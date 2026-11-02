package federation

import (
	"encoding/xml"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// TokenWebDAVHandler serves files via token-based auth for federated shares.
// Remote servers access our files at /public.php/webdav/ using the shared
// secret token from the OCM share.
type TokenWebDAVHandler struct {
	Log     *slog.Logger
	Store   *Store
	Shares  *share.Store
	Users   *user.Store
	Files   *files.Plugin
	DataDir string
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
		h.servePublicShare(w, r, shareRec)
		return
	}

	// Only outbound shares should be accessed by remote servers.
	if fs.Direction != "outbound" {
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

	// Build the local filesystem path used only for PROPFIND read-listing and
	// GET/HEAD serving. Writes go through the files plugin (see handlePut/Mkcol/Delete)
	// so they update DB metadata, quota, indexing, and activity.
	filesDir := filepath.Join(h.DataDir, owner.Username, "files")
	basePath := filepath.Join(filesDir, filepath.FromSlash(resourcePath))

	// Extract the sub-path from the request URL (slash-form, relative to the
	// shared resource root).
	subPath := strings.TrimPrefix(r.URL.Path, "/public.php/webdav")
	subPath = path.Clean("/" + subPath)
	localPath := filepath.Join(basePath, filepath.FromSlash(subPath))

	// Security: ensure we don't escape the share root.
	if localPath != basePath && !strings.HasPrefix(localPath, basePath+string(os.PathSeparator)) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	// Owner-namespace path passed to the files plugin (slash-form).
	targetPath := path.Join(resourcePath, subPath)

	// Enforce permissions.
	isReadOnly := rec.Permissions&2 == 0 // no write permission
	if isReadOnly && r.Method != http.MethodGet && r.Method != "PROPFIND" &&
		r.Method != http.MethodHead && r.Method != "OPTIONS" {
		http.Error(w, "Read-only share", http.StatusForbidden)
		return
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, r, localPath, basePath, subPath)
	case http.MethodGet, http.MethodHead:
		http.ServeFile(w, r, localPath)
	case http.MethodPut:
		h.handlePut(w, r, owner.Username, targetPath)
	case "MKCOL":
		h.handleMkcol(w, owner.Username, targetPath)
	case http.MethodDelete:
		h.handleDelete(w, owner.Username, targetPath)
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

func (h *TokenWebDAVHandler) extractToken(r *http.Request) string {
	// Try Bearer token.
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	// Try Basic Auth (empty username, token as password).
	_, password, ok := r.BasicAuth()
	if ok && password != "" {
		return password
	}
	return ""
}

func (h *TokenWebDAVHandler) handlePropfind(w http.ResponseWriter, r *http.Request, localPath, basePath, subPath string) {
	info, err := os.Stat(localPath)
	if errors.Is(err, fs.ErrNotExist) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Error("federated propfind: stat", "path", localPath, "err", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Read the children before the 207 goes out, so a failure can still
	// be reported as one.
	var entries []os.DirEntry
	if info.IsDir() && r.Header.Get("Depth") != "0" {
		if entries, err = os.ReadDir(localPath); err != nil {
			h.Log.Error("federated propfind: read directory", "path", localPath, "err", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207) // Multi-Status

	href := "/public.php/webdav" + subPath

	io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>`)
	io.WriteString(w, `<d:multistatus xmlns:d="DAV:">`)

	writePropResponse(w, href, info)

	for _, entry := range entries {
		childInfo, err := entry.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since ReadDir
		}
		if err != nil {
			// The 207 is already sent; the listing goes out without this entry.
			h.Log.Error("federated propfind: stat entry", "path", filepath.Join(localPath, entry.Name()), "err", err)
			continue
		}
		childHref := path.Join(href, entry.Name())
		if entry.IsDir() {
			childHref += "/"
		}
		writePropResponse(w, childHref, childInfo)
	}

	io.WriteString(w, `</d:multistatus>`)
}

func writePropResponse(w io.Writer, href string, info os.FileInfo) {
	resourceType := ""
	if info.IsDir() {
		resourceType = "<d:collection/>"
	}
	// href incorporates the filename which is user-controlled; XML-escape it.
	io.WriteString(w, `<d:response>`)
	io.WriteString(w, `<d:href>`+xmlEscape(href)+`</d:href>`)
	io.WriteString(w, `<d:propstat>`)
	io.WriteString(w, `<d:prop>`)
	io.WriteString(w, `<d:resourcetype>`+resourceType+`</d:resourcetype>`)
	if !info.IsDir() {
		io.WriteString(w, `<d:getcontentlength>`+strconv.FormatInt(info.Size(), 10)+`</d:getcontentlength>`)
	}
	io.WriteString(w, `<d:getlastmodified>`+info.ModTime().UTC().Format(http.TimeFormat)+`</d:getlastmodified>`)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *TokenWebDAVHandler) handleMkcol(w http.ResponseWriter, username, targetPath string) {
	if _, err := h.Files.CreateDir(username, targetPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *TokenWebDAVHandler) handleDelete(w http.ResponseWriter, username, targetPath string) {
	if err := h.Files.Delete(username, targetPath); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
