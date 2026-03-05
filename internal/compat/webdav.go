package compat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// directToken is a short-lived token for unauthenticated file downloads.
type directToken struct {
	Username string
	FilePath string
	Created  time.Time
}

// WebDAVHandler implements NextCloud-compatible WebDAV for the files plugin.
type WebDAVHandler struct {
	Files *files.Plugin
	Users *user.Store

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
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	hrefPrefix := "/remote.php/dav/files/" + urlUserID

	var entries []propfindEntry
	href := hrefPrefix + filePath
	if rootMeta.IsDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}
	entries = append(entries, metaToEntry(href, rootMeta))

	// If depth=1 and target is a directory, list children
	if depth == "1" && rootMeta.IsDir {
		children, err := h.Files.List(u.Username, filePath)
		if err == nil {
			for i := range children {
				child := &children[i]
				childHref := hrefPrefix + child.Path
				if child.IsDir && !strings.HasSuffix(childHref, "/") {
					childHref += "/"
				}
				entries = append(entries, metaToEntry(childHref, child))
			}
		}
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("DAV", "1, 3, extended-mkcol")
	w.WriteHeader(207) // Multi-Status

	io.WriteString(w, xml.Header)
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(multiStatus{
		DAV:       "DAV:",
		OC:        "http://owncloud.org/ns",
		NC:        "http://nextcloud.org/ns",
		Responses: entries,
	})
	enc.Flush()
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
		// Check if it's a directory
		if r.Method == http.MethodHead {
			if m, _ := h.Files.GetByPath(u.Username, filePath); m != nil && m.IsDir {
				w.Header().Set("DAV", "1, 3, extended-mkcol")
				w.WriteHeader(http.StatusOK)
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
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
		log.Printf("webdav: GET copy error: %v", err)
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

	if err := h.Files.Delete(u.Username, filePath); err != nil {
		w.WriteHeader(http.StatusNotFound)
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
	destMeta, _ := h.Files.GetByPath(u.Username, newPath)
	if destMeta != nil && overwrite == "F" {
		http.Error(w, "destination exists", http.StatusPreconditionFailed)
		return
	}

	srcMeta, err := h.Files.GetByPath(u.Username, srcPath)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	if srcMeta.IsDir {
		if err := h.copyDir(u.Username, srcPath, newPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else {
		if err := h.copyFile(u.Username, srcPath, newPath); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		if err != nil {
			writeOCSError(w, http.StatusNotFound, 404, "file not found")
			return
		}

		token := h.createDirectToken(u.Username, meta.Path)
		url := baseURL + "/remote.php/dav/direct/" + token
		writeOCS(w, http.StatusOK, map[string]any{
			"url": url,
		})
	}
}

func (h *WebDAVHandler) createDirectToken(username, filePath string) string {
	b := make([]byte, 16)
	rand.Read(b)
	token := hex.EncodeToString(b)

	h.directMu.Lock()
	defer h.directMu.Unlock()
	if h.directTokens == nil {
		h.directTokens = make(map[string]directToken)
	}
	// Evict expired tokens (older than 10 minutes)
	for k, v := range h.directTokens {
		if time.Since(v.Created) > 10*time.Minute {
			delete(h.directTokens, k)
		}
	}
	h.directTokens[token] = directToken{
		Username: username,
		FilePath: filePath,
		Created:  time.Now(),
	}
	return token
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

	if !ok || time.Since(dt.Created) > 10*time.Minute {
		http.Error(w, "invalid or expired token", http.StatusNotFound)
		return
	}

	f, meta, err := h.Files.Download(dt.Username, dt.FilePath)
	if err != nil {
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
		log.Printf("webdav: direct download copy error: %v", err)
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
	DisplayName   string        `xml:"D:displayname"`
	ContentType   string        `xml:"D:getcontenttype,omitempty"`
	ContentLength int64         `xml:"D:getcontentlength"`
	ETag          string        `xml:"D:getetag,omitempty"`
	LastModified  string        `xml:"D:getlastmodified,omitempty"`
	ResourceType  *resourceType `xml:"D:resourcetype"`
	OCId          string        `xml:"oc:id"`
	OCFileID      string        `xml:"oc:fileid"`
	OCPermissions string        `xml:"oc:permissions"`
	OCSize        int64         `xml:"oc:size"`
	NCHasPreview  bool          `xml:"nc:has-preview"`
}

type resourceType struct {
	Collection *struct{} `xml:"D:collection,omitempty"`
}

func metaToEntry(href string, meta *files.FileMeta) propfindEntry {
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

	return propfindEntry{
		Href: href,
		Propstat: propstat{
			Status: "HTTP/1.1 200 OK",
			Prop: prop{
				DisplayName:   name,
				ContentType:   meta.MimeType,
				ContentLength: meta.Size,
				ETag:          `"` + meta.ETag + `"`,
				LastModified:  time.Unix(meta.MTime, 0).UTC().Format(http.TimeFormat),
				ResourceType:  &rt,
				OCId:          fmt.Sprintf("%08d", meta.ID),
				OCFileID:      strconv.FormatInt(meta.ID, 10),
				OCPermissions: permissions,
				OCSize:        meta.Size,
				NCHasPreview:  files.HasPreview(meta.MimeType),
			},
		},
	}
}
