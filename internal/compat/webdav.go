package compat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// directToken is a short-lived token for unauthenticated file downloads.
type directToken struct {
	Username string
	FilePath string
	Created  time.Time
}

// WebDAVHandler implements NextCloud-compatible WebDAV for the files plugin.
type WebDAVHandler struct {
	Log   *slog.Logger
	Files *files.Plugin
	Clock clock.Clock

	directMu     sync.Mutex
	directTokens map[string]directToken
}

func (h *WebDAVHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Extract file path and the user ID from the URL (may be stale/cached).
	urlUserID, filePath := h.extractPath(r.URL.Path)
	// Use the URL's user ID for response hrefs so clients can reconcile responses
	// with their requests, even if the user ID changed (e.g. after server reset).
	if urlUserID == "" {
		urlUserID = u.ID
	}

	// Ensure the user's root directory exists.
	if err := h.Files.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, r, u, urlUserID, filePath)
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, u, filePath)
	case http.MethodPut:
		h.handlePut(w, r, u, filePath)
	case http.MethodDelete:
		h.handleDelete(w, r, u, filePath)
	case "MKCOL":
		h.handleMkcol(w, r, u, filePath)
	case "MOVE":
		h.handleMove(w, r, u, filePath)
	case "COPY":
		h.handleCopy(w, r, u, filePath)
	case "PROPPATCH":
		h.handleProppatch(w, r, u, urlUserID, filePath)
	case "REPORT":
		h.handleReport(w, r, u, urlUserID, filePath)
	case "LOCK":
		h.handleLock(w, r, u, filePath)
	case "UNLOCK":
		h.handleUnlock(w, r, u, filePath)
	default:
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// extractPath strips the WebDAV prefix to get the user ID and file path.
// Path format: /remote.php/dav/files/{user_id}/path/to/file
// The user_id in the URL may be stale (NC clients cache it), so we return
// it separately for use in response hrefs while relying on the authenticated
// user for authorization.
func (h *WebDAVHandler) extractPath(urlPath string) (urlUserID string, filePath string) {
	const prefix = "/remote.php/dav/files/"
	rest, ok := strings.CutPrefix(urlPath, prefix)
	if !ok {
		return "", "/"
	}
	// Split into user_id and file path
	if uid, fp, ok := strings.Cut(rest, "/"); ok {
		// Unescape percent-encoded characters (e.g. %20 → space).
		// r.URL.Path is already decoded, but the Destination header in
		// MOVE/COPY is a raw URL that may contain percent-encoding.
		decoded, err := url.PathUnescape(fp)
		if err == nil {
			fp = decoded
		}
		return uid, path.Clean("/" + fp)
	}
	return rest, "/"
}

// --- PROPFIND ---

func (h *WebDAVHandler) handlePropfind(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	depth := r.Header.Get("Depth")
	if depth == "" {
		depth = "1"
	}

	// Get metadata for the target path
	rootMeta, err := h.Files.GetByPath(u.Username, filePath)
	if errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Error("PROPFIND", "user", u.Username, "path", filePath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	hrefPrefix := "/remote.php/dav/files/" + urlUserID

	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	var entries []propfindEntry
	href := hrefPrefix + filePath
	if rootMeta.IsDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}
	entries = append(entries, metaToEntry(href, rootMeta, u.Username, displayName))

	// If depth=1 and target is a directory, list children
	if depth == "1" && rootMeta.IsDir {
		children, err := h.Files.List(u.Username, filePath)
		if err != nil {
			h.Log.Error("PROPFIND list", "user", u.Username, "path", filePath, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for i := range children {
			child := &children[i]
			childHref := hrefPrefix + child.Path
			if child.IsDir && !strings.HasSuffix(childHref, "/") {
				childHref += "/"
			}
			entries = append(entries, metaToEntry(childHref, child, u.Username, displayName))
		}
	}

	w.Header().Set("DAV", "1, 3, extended-mkcol")
	writeMultiStatus(h.Log, w, entries)
}

// --- GET ---

func (h *WebDAVHandler) handleGet(w http.ResponseWriter, r *http.Request, u *user.User, filePath string) {
	if filePath == "/" {
		// HEAD on root is a presence check; GET on a directory is not supported.
		if r.Method == http.MethodHead {
			w.Header().Set("DAV", "1, 3, extended-mkcol")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	f, meta, err := h.Files.Download(u.Username, filePath)
	if err != nil {
		// A directory has metadata but can't be opened as a file.
		m, merr := h.Files.GetByPath(u.Username, filePath)
		switch {
		case errors.Is(merr, files.ErrNotFound):
			w.WriteHeader(http.StatusNotFound)
		case merr != nil:
			h.Log.Error("GET", "user", u.Username, "path", filePath, "err", merr)
			w.WriteHeader(http.StatusInternalServerError)
		case m.IsDir && r.Method == http.MethodHead:
			w.Header().Set("DAV", "1, 3, extended-mkcol")
			w.WriteHeader(http.StatusOK)
		case m.IsDir:
			w.WriteHeader(http.StatusNotFound)
		default:
			h.Log.Error("GET", "user", u.Username, "path", filePath, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	defer f.Close()

	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Last-Modified", time.Unix(meta.MTime, 0).UTC().Format(http.TimeFormat))

	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		h.Log.Error("GET copy error", "err", err)
	}
}

// --- PUT ---

func (h *WebDAVHandler) handlePut(w http.ResponseWriter, r *http.Request, u *user.User, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot PUT to root", http.StatusBadRequest)
		return
	}

	meta, err := h.Files.Upload(u.Username, filePath, r.Body)
	if err != nil {
		h.Log.Error("PUT", "user", u.Username, "path", filePath, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("OC-FileId", fmt.Sprintf("%08d", meta.ID))
	w.WriteHeader(http.StatusCreated)
}

// --- DELETE ---

func (h *WebDAVHandler) handleDelete(w http.ResponseWriter, _ *http.Request, u *user.User, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot delete root", http.StatusForbidden)
		return
	}

	if err := h.Files.Delete(u.Username, filePath); errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	} else if err != nil {
		h.Log.Error("DELETE", "user", u.Username, "path", filePath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MKCOL ---

func (h *WebDAVHandler) handleMkcol(w http.ResponseWriter, _ *http.Request, u *user.User, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot MKCOL root", http.StatusMethodNotAllowed)
		return
	}

	_, err := h.Files.CreateDir(u.Username, filePath)
	if err != nil {
		h.Log.Warn("MKCOL", "user", u.Username, "path", filePath, "err", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// --- MOVE ---

func (h *WebDAVHandler) handleMove(w http.ResponseWriter, r *http.Request, u *user.User, filePath string) {
	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "missing Destination header", http.StatusBadRequest)
		return
	}

	// Destination is a full URL; extract the path portion
	destPath := dest
	if idx := strings.Index(dest, "/remote.php/"); idx >= 0 {
		destPath = dest[idx:]
	}
	// Strip the /remote.php/dav/files/{user_id}/ prefix
	_, newPath := h.extractPath(destPath)

	_, err := h.Files.Rename(u.Username, filePath, newPath)
	if err != nil {
		h.Log.Warn("MOVE", "user", u.Username, "from", filePath, "to", newPath, "err", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// --- COPY ---

func (h *WebDAVHandler) handleCopy(w http.ResponseWriter, r *http.Request, u *user.User, srcPath string) {
	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "missing Destination header", http.StatusBadRequest)
		return
	}

	destPath := dest
	if idx := strings.Index(dest, "/remote.php/"); idx >= 0 {
		destPath = dest[idx:]
	}
	_, newPath := h.extractPath(destPath)

	overwrite := r.Header.Get("Overwrite")
	if overwrite == "" {
		overwrite = "T"
	}

	// Check if destination exists.
	destMeta, err := h.Files.GetByPath(u.Username, newPath)
	if err != nil && !errors.Is(err, files.ErrNotFound) {
		h.Log.Error("COPY: look up destination", "user", u.Username, "path", newPath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if destMeta != nil && overwrite == "F" {
		http.Error(w, "destination exists", http.StatusPreconditionFailed)
		return
	}

	srcMeta, err := h.Files.GetByPath(u.Username, srcPath)
	if errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Error("COPY: look up source", "user", u.Username, "path", srcPath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if srcMeta.IsDir {
		err = h.copyDir(u.Username, srcPath, newPath)
	} else {
		err = h.copyFile(u.Username, srcPath, newPath)
	}
	if err != nil {
		h.Log.Error("COPY", "user", u.Username, "from", srcPath, "to", newPath, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if destMeta != nil {
		w.WriteHeader(http.StatusNoContent) // overwritten
	} else {
		w.WriteHeader(http.StatusCreated)
	}
}

func (h *WebDAVHandler) copyFile(username, srcPath, destPath string) error {
	f, _, err := h.Files.Download(username, srcPath)
	if err != nil {
		return fmt.Errorf("copy download: %w", err)
	}
	defer f.Close()

	_, err = h.Files.Upload(username, destPath, f)
	return err
}

func (h *WebDAVHandler) copyDir(username, srcPath, destPath string) error {
	if _, err := h.Files.CreateDir(username, destPath); err != nil {
		return fmt.Errorf("copy mkdir: %w", err)
	}

	children, err := h.Files.List(username, srcPath)
	if err != nil {
		return fmt.Errorf("copy list: %w", err)
	}

	for i := range children {
		child := &children[i]
		childDest := destPath + "/" + child.Name
		if child.IsDir {
			if err := h.copyDir(username, child.Path, childDest); err != nil {
				return err
			}
		} else {
			if err := h.copyFile(username, child.Path, childDest); err != nil {
				return err
			}
		}
	}
	return nil
}

// HandleDirectDownload handles POST /ocs/v2.php/apps/dav/api/v1/direct.
// The NC Android app POSTs fileId=N to get a token-based direct download URL
// that can be fetched without authentication (used by the media player).
func (h *WebDAVHandler) HandleDirectDownload(baseURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil {
			writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
			return
		}

		fileIDStr := r.FormValue("fileId")
		fileID, err := strconv.ParseInt(fileIDStr, 10, 64)
		if err != nil || fileID <= 0 {
			writeOCSError(w, http.StatusBadRequest, 400, "invalid fileId")
			return
		}

		meta, err := h.Files.GetByID(u.Username, fileID)
		if errors.Is(err, files.ErrNotFound) {
			writeOCSError(w, http.StatusNotFound, 404, "file not found")
			return
		}
		if err != nil {
			h.Log.Error("direct download: look up file", "user", u.Username, "file", fileID, "err", err)
			writeOCSError(w, http.StatusInternalServerError, 500, "look up file failed")
			return
		}

		token, err := h.createDirectToken(u.Username, meta.Path)
		if err != nil {
			writeOCSError(w, http.StatusInternalServerError, 500, "create token: "+err.Error())
			return
		}
		url := baseURL + "/remote.php/dav/direct/" + token
		writeOCS(w, http.StatusOK, map[string]any{
			"url": url,
		})
	}
}

func (h *WebDAVHandler) createDirectToken(username, filePath string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate direct token: %w", err)
	}
	token := hex.EncodeToString(b)

	h.directMu.Lock()
	defer h.directMu.Unlock()
	if h.directTokens == nil {
		h.directTokens = make(map[string]directToken)
	}
	// Evict expired tokens (older than 10 minutes)
	for k, v := range h.directTokens {
		if h.Clock().Sub(v.Created) > 10*time.Minute {
			delete(h.directTokens, k)
		}
	}
	h.directTokens[token] = directToken{
		Username: username,
		FilePath: filePath,
		Created:  h.Clock(),
	}
	return token, nil
}

// HandleDirectServe handles GET /remote.php/dav/direct/{token}.
// No authentication required — the token itself authorizes the download.
func (h *WebDAVHandler) HandleDirectServe(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/direct/")
	if token == "" {
		http.Error(w, "missing token", http.StatusBadRequest)
		return
	}

	h.directMu.Lock()
	dt, ok := h.directTokens[token]
	if ok {
		delete(h.directTokens, token) // one-time use
	}
	h.directMu.Unlock()

	if !ok || h.Clock().Sub(dt.Created) > 10*time.Minute {
		http.Error(w, "invalid or expired token", http.StatusNotFound)
		return
	}

	f, meta, err := h.Files.Download(dt.Username, dt.FilePath)
	if err != nil {
		h.Log.Warn("direct download", "user", dt.Username, "path", dt.FilePath, "err", err)
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.Name))
	if _, err := io.Copy(w, f); err != nil {
		h.Log.Error("direct download copy error", "err", err)
	}
}

// --- XML types for PROPFIND responses ---

type multiStatus struct {
	XMLName   xml.Name        `xml:"D:multistatus"`
	DAV       string          `xml:"xmlns:D,attr"`
	OC        string          `xml:"xmlns:oc,attr"`
	NC        string          `xml:"xmlns:nc,attr"`
	Responses []propfindEntry `xml:"D:response"`
}

type propfindEntry struct {
	Href     string   `xml:"D:href"`
	Propstat propstat `xml:"D:propstat"`
}

type propstat struct {
	Prop   prop   `xml:"D:prop"`
	Status string `xml:"D:status"`
}

type prop struct {
	DisplayName        string        `xml:"D:displayname"`
	ContentType        string        `xml:"D:getcontenttype,omitempty"`
	ContentLength      int64         `xml:"D:getcontentlength"`
	ETag               string        `xml:"D:getetag,omitempty"`
	LastModified       string        `xml:"D:getlastmodified,omitempty"`
	ResourceType       *resourceType `xml:"D:resourcetype"`
	OCId               string        `xml:"oc:id"`
	OCFileID           string        `xml:"oc:fileid"`
	OCPermissions      string        `xml:"oc:permissions"`
	OCSize             int64         `xml:"oc:size"`
	NCHasPreview       bool          `xml:"nc:has-preview"`
	OCFavorite         int           `xml:"oc:favorite"`
	OCCommentsCount    int           `xml:"oc:comments-count"`
	OCCommentsUnread   int           `xml:"oc:comments-unread"`
	NCMountType        string        `xml:"nc:mount-type"`
	OCOwnerID          string        `xml:"oc:owner-id"`
	OCOwnerDisplayName string        `xml:"oc:owner-display-name"`
	NCSharees          *struct{}     `xml:"nc:sharees"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

// --- PROPPATCH ---

func (h *WebDAVHandler) handleProppatch(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Parse the PROPPATCH request to find oc:favorite
	type proppatchSet struct {
		Prop struct {
			Favorite *int `xml:"favorite"`
		} `xml:"prop"`
	}
	type proppatchReq struct {
		XMLName xml.Name     `xml:"propertyupdate"`
		Set     proppatchSet `xml:"set"`
	}
	var req proppatchReq
	if err := xml.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse xml", http.StatusBadRequest)
		return
	}

	if req.Set.Prop.Favorite != nil {
		fav := *req.Set.Prop.Favorite != 0
		if err := h.Files.SetFavorite(u.Username, filePath, fav); errors.Is(err, files.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		} else if err != nil {
			h.Log.Error("PROPPATCH favorite", "user", u.Username, "path", filePath, "err", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Return 207 Multi-Status response. Escape the href since filePath
	// is user-controlled (filenames can contain "<", "&", etc.) — audit
	// security.md #14.
	hrefPrefix := "/remote.php/dav/files/" + urlUserID
	href := xmlEscape(hrefPrefix + filePath)

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)

	io.WriteString(w, xml.Header)
	fmt.Fprintf(w, `<d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
  <d:response>
    <d:href>%s</d:href>
    <d:propstat>
      <d:prop>
        <oc:favorite/>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`, href)
}

// --- REPORT ---

func (h *WebDAVHandler) handleReport(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}

	// Check if this is a filter-files report for favorites
	if !strings.Contains(string(body), "filter-files") {
		w.WriteHeader(http.StatusNotImplemented)
		return
	}

	favorites, err := h.Files.Favorites(u.Username)
	if err != nil {
		h.Log.Error("REPORT favorites", "user", u.Username, "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	hrefPrefix := "/remote.php/dav/files/" + urlUserID
	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	var entries []propfindEntry
	for i := range favorites {
		f := &favorites[i]
		href := hrefPrefix + f.Path
		if f.IsDir && !strings.HasSuffix(href, "/") {
			href += "/"
		}
		entries = append(entries, metaToEntry(href, f, u.Username, displayName))
	}

	writeMultiStatus(h.Log, w, entries)
}

// writeMultiStatus sends entries as a 207 Multi-Status response.
func writeMultiStatus(log *slog.Logger, w http.ResponseWriter, entries []propfindEntry) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)

	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	if err := enc.Encode(multiStatus{
		DAV:       "DAV:",
		OC:        "http://owncloud.org/ns",
		NC:        "http://nextcloud.org/ns",
		Responses: entries,
	}); err != nil {
		// The status is already sent; all that's left is to say why the
		// body is short.
		log.Error("write multistatus", "err", err)
	}
}

// --- LOCK (stub) ---

func (h *WebDAVHandler) handleLock(w http.ResponseWriter, r *http.Request, u *user.User, filePath string) {
	// Stub: return a valid lock-discovery response so clients think locking succeeded.
	// username and filePath are user-controlled; escape both for XML output.
	token := "opaquelocktoken:stub-" + u.Username + "-" + filePath
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Lock-Token", "<"+token+">")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, xml.Header)
	fmt.Fprintf(w, `<D:prop xmlns:D="DAV:">
  <D:lockdiscovery>
    <D:activelock>
      <D:locktype><D:write/></D:locktype>
      <D:lockscope><D:exclusive/></D:lockscope>
      <D:depth>0</D:depth>
      <D:owner><D:href>%s</D:href></D:owner>
      <D:timeout>Second-1800</D:timeout>
      <D:locktoken><D:href>%s</D:href></D:locktoken>
    </D:activelock>
  </D:lockdiscovery>
</D:prop>`, xmlEscape(u.Username), xmlEscape(token))
}

// --- UNLOCK (stub) ---

func (h *WebDAVHandler) handleUnlock(w http.ResponseWriter, _ *http.Request, _ *user.User, _ string) {
	w.WriteHeader(http.StatusNoContent)
}

func metaToEntry(href string, meta *files.FileMeta, ownerID, ownerDisplayName string) propfindEntry {
	var rt resourceType
	if meta.IsDir {
		rt.Collection = &struct{}{}
	}

	name := meta.Name
	if meta.Path == "/" {
		name = ""
	}

	permissions := "RGDNVCK" // full permissions for owned files
	if meta.IsDir {
		permissions = "RGDNVCK"
	}

	favInt := 0
	if meta.Favorite {
		favInt = 1
	}

	return propfindEntry{
		Href: href,
		Propstat: propstat{
			Status: "HTTP/1.1 200 OK",
			Prop: prop{
				DisplayName:        name,
				ContentType:        meta.MimeType,
				ContentLength:      meta.Size,
				ETag:               `"` + meta.ETag + `"`,
				LastModified:       time.Unix(meta.MTime, 0).UTC().Format(http.TimeFormat),
				ResourceType:       &rt,
				OCId:               fmt.Sprintf("%08d", meta.ID),
				OCFileID:           strconv.FormatInt(meta.ID, 10),
				OCPermissions:      permissions,
				OCSize:             meta.Size,
				NCHasPreview:       files.HasPreview(meta.MimeType),
				OCFavorite:         favInt,
				NCMountType:        "",
				OCOwnerID:          ownerID,
				OCOwnerDisplayName: ownerDisplayName,
				NCSharees:          &struct{}{},
			},
		},
	}
}
