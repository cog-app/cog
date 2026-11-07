package federation

import (
	"encoding/xml"
	"errors"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// TokenWebDAVHandler serves files via token-based auth for federated shares.
// Remote servers access our files at /public.php/webdav/ using the shared
// secret token from the OCM share.
type TokenWebDAVHandler struct {
	Log    *slog.Logger
	Store  *Store
	Shares *share.Store
	Users  *user.Store
	Files  *files.Plugin
	Policy *Policy
	// Guard throttles guessing a public link's password here as it does on
	// the link's page. A nil Guard allows every attempt.
	Guard *ratelimit.Guard
}

func (h *TokenWebDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Authenticate via token.
	token := h.extractToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", `Basic realm="federation"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Look up the share by token. Check federated_shares first, then regular shares.
	fs, err := h.Store.GetByToken(token)
	if err != nil && !errors.Is(err, ErrNotFound) {
		h.serverError(w, "look up federated share", err)
		return
	}
	if err != nil {
		// Fall back to regular public link share.
		shareRec, err := h.Shares.GetByToken(token)
		if errors.Is(err, share.ErrNotFound) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if err != nil {
			h.serverError(w, "look up share", err)
			return
		}
		// Only a public link names one, and only a file or folder's is
		// served here: another type's resource ID (a calendar's, whose
		// link is a published feed) would be read as a file ID.
		if shareRec.SubjectType != "PUBLIC" || !isFileShare(shareRec) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// A link with a password asks for it as Nextcloud does: the
		// token is the Basic username and the password its password.
		if shareRec.PasswordHash != nil && *shareRec.PasswordHash != "" {
			user, pass, hasAuth := r.BasicAuth()
			ok := false
			if hasAuth && user == token {
				var err error
				ok, err = h.Guard.Try(ratelimit.ClientIP(r), ratelimit.ShareKey(shareRec.ID), pass, func() (bool, error) {
					return share.CheckPassword(shareRec, pass) == nil, nil
				})
				if err != nil && !errors.Is(err, ratelimit.ErrLimited) {
					h.serverError(w, "check share password", err)
					return
				}
			}
			if !ok {
				w.Header().Set("WWW-Authenticate", `Basic realm="share"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h.servePublicShare(w, r, shareRec)
		return
	}

	// Only outbound shares should be accessed by remote servers.
	if fs.Direction != "outbound" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	// Only while this server still federates with the remote.
	allowed, err := h.Policy.PeerAllowed(fs.RemoteHost)
	if err != nil {
		h.serverError(w, "read federation policy", err)
		return
	}
	if !allowed {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Resolve the share to a local file/directory.
	if fs.ShareID == nil {
		http.Error(w, "Share not linked", http.StatusInternalServerError)
		return
	}
	shareRec, err := h.Shares.Get(*fs.ShareID)
	if errors.Is(err, share.ErrNotFound) {
		http.Error(w, "Share not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.serverError(w, "look up share", err)
		return
	}
	if !isFileShare(shareRec) {
		http.Error(w, "Share not found", http.StatusNotFound)
		return
	}

	h.servePublicShare(w, r, shareRec)
}

func (h *TokenWebDAVHandler) servePublicShare(w http.ResponseWriter, r *http.Request, rec *share.Record) {
	// Resolve the owner user.
	owner, err := h.Users.GetByID(rec.OwnerUserID)
	if err != nil {
		h.serverError(w, "look up share owner", err)
		return
	}

	// Get the shared resource path from the files plugin.
	resourceID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		http.Error(w, "Invalid resource ID", http.StatusInternalServerError)
		return
	}
	meta, err := h.Files.GetByID(owner.Username, resourceID)
	if errors.Is(err, files.ErrNotFound) {
		http.Error(w, "Resource not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.serverError(w, "look up shared resource", err)
		return
	}
	resourcePath := meta.Path

	// Extract the sub-path from the request URL (slash-form, relative to the
	// shared resource root). Everything below goes through the files plugin
	// in the owner's namespace, so the owner's mounts decide where the bytes
	// live, as they do for the owner's own WebDAV.
	subPath := strings.TrimPrefix(r.URL.Path, "/public.php/webdav")
	subPath = path.Clean("/" + subPath)
	targetPath := path.Join(resourcePath, subPath)

	// Security: ensure we don't escape the share root. A shared file has no
	// children to reach.
	if !within(targetPath, resourcePath) || (!meta.IsDir && subPath != "/") {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Enforce permissions.
	isReadOnly := rec.Permissions&2 == 0 // no write permission
	if isReadOnly && r.Method != http.MethodGet && r.Method != "PROPFIND" &&
		r.Method != http.MethodHead && r.Method != "OPTIONS" {
		http.Error(w, "Read-only share", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, r, owner.Username, targetPath, subPath)
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, owner.Username, targetPath)
	case http.MethodPut:
		h.handlePut(w, r, owner.Username, targetPath)
	case "MKCOL":
		h.handleMkcol(w, r, owner.Username, targetPath)
	case http.MethodDelete:
		h.handleDelete(w, r, owner.Username, targetPath)
	case "OPTIONS":
		w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, PROPFIND")
		w.Header().Set("DAV", "1, 2")
		w.WriteHeader(http.StatusOK)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// serverError logs a failure the client cannot act on and reports a 500.
func (h *TokenWebDAVHandler) serverError(w http.ResponseWriter, what string, err error) {
	h.Log.Error("federated webdav: "+what, "err", err)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
}

// isFileShare reports whether rec shares a file or folder, the only
// resources this endpoint serves.
func isFileShare(rec *share.Record) bool {
	return rec.ResourceType == "files:file" || rec.ResourceType == "files:directory"
}

func (h *TokenWebDAVHandler) extractToken(r *http.Request) string {
	// Try Bearer token.
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Basic Auth: a federated server sends the token as the password with
	// no username; a public link client sends it as the username, with the
	// link's password, if any, as the password.
	username, password, ok := r.BasicAuth()
	if !ok {
		return ""
	}
	if username != "" {
		return username
	}
	return password
}

// within reports whether p is root or below it; both are clean slash paths.
func within(p, root string) bool {
	return root == "/" || p == root || strings.HasPrefix(p, root+"/")
}

func (h *TokenWebDAVHandler) handlePropfind(w http.ResponseWriter, r *http.Request, username, targetPath, subPath string) {
	meta, err := h.Files.GetByPath(username, targetPath)
	if errors.Is(err, files.ErrNotFound) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.serverError(w, "propfind: look up "+targetPath, err)
		return
	}

	// Read the children before the 207 goes out, so a failure can still
	// be reported as one.
	var children []files.FileMeta
	if meta.IsDir && r.Header.Get("Depth") != "0" {
		if children, err = h.Files.List(username, targetPath); err != nil {
			h.serverError(w, "propfind: list "+targetPath, err)
			return
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207) // Multi-Status

	href := "/public.php/webdav" + subPath
	if meta.IsDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}

	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`)
	io.WriteString(w, `<d:multistatus xmlns:d="DAV:">`)

	writePropResponse(w, href, meta)
	for i := range children {
		child := &children[i]
		childHref := path.Join(href, child.Name)
		if child.IsDir {
			childHref += "/"
		}
		writePropResponse(w, childHref, child)
	}

	io.WriteString(w, `</d:multistatus>`)
}

func (h *TokenWebDAVHandler) handleGet(w http.ResponseWriter, r *http.Request, username, targetPath string) {
	meta, err := h.Files.GetByPath(username, targetPath)
	if errors.Is(err, files.ErrNotFound) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.serverError(w, "get: look up "+targetPath, err)
		return
	}
	if meta.IsDir {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	rc, meta, err := h.Files.Download(username, targetPath)
	if errors.Is(err, files.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.serverError(w, "get: open "+targetPath, err)
		return
	}
	defer rc.Close()

	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	cogplugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	modTime := time.Unix(meta.MTime, 0)

	// A seekable body (a local file) gets ranges and conditional requests.
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(w, r, meta.Name, modTime, rs)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Last-Modified", modTime.UTC().Format(http.TimeFormat))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, rc); err != nil {
		// The status is already sent; all that's left is to record it.
		h.Log.Error("federated webdav: get: copy", "path", targetPath, "err", err)
	}
}

func writePropResponse(w io.Writer, href string, meta *files.FileMeta) {
	resourceType := ""
	if meta.IsDir {
		resourceType = "<d:collection/>"
	}
	// href incorporates the filename which is user-controlled; XML-escape it.
	io.WriteString(w, `<d:response>`)
	io.WriteString(w, `<d:href>`+xmlEscape(href)+`</d:href>`)
	io.WriteString(w, `<d:propstat>`)
	io.WriteString(w, `<d:prop>`)
	io.WriteString(w, `<d:resourcetype>`+resourceType+`</d:resourcetype>`)
	if !meta.IsDir {
		io.WriteString(w, `<d:getcontentlength>`+strconv.FormatInt(meta.Size, 10)+`</d:getcontentlength>`)
	}
	io.WriteString(w, `<d:getlastmodified>`+time.Unix(meta.MTime, 0).UTC().Format(http.TimeFormat)+`</d:getlastmodified>`)
	io.WriteString(w, `</d:prop>`)
	io.WriteString(w, `<d:status>HTTP/1.1 200 OK</d:status>`)
	io.WriteString(w, `</d:propstat>`)
	io.WriteString(w, `</d:response>`)
}

// xmlEscape escapes a string for inclusion as element content or quoted
// attribute value in XML output.
func xmlEscape(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (h *TokenWebDAVHandler) handlePut(w http.ResponseWriter, r *http.Request, username, targetPath string) {
	if _, err := h.Files.Upload(username, targetPath, r.Body); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *TokenWebDAVHandler) handleMkcol(w http.ResponseWriter, r *http.Request, username, targetPath string) {
	if _, err := h.Files.CreateDir(username, targetPath); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *TokenWebDAVHandler) handleDelete(w http.ResponseWriter, r *http.Request, username, targetPath string) {
	if err := h.Files.Delete(username, targetPath); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
