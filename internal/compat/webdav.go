package compat

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/httphdr"
	"git.duckfam.us/jj-software/cog/internal/user"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"
)

// directTokenLifetime is how long a direct-download link works. It works
// once.
const directTokenLifetime = 10 * time.Minute

// WebDAVHandler implements NextCloud-compatible WebDAV for the files plugin.
type WebDAVHandler struct {
	Log   *slog.Logger
	Files *files.Plugin
	Clock clock.Clock
	// DB is the system database, which holds direct-download tokens, so
	// a link outlives a restart and works on any replica.
	DB *sql.DB
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
		httperr.Internal(w, r, err)
		return
	}

	switch r.Method {
	case "PROPFIND":
		h.handlePropfind(w, r, u, urlUserID, filePath)
	case http.MethodGet, http.MethodHead:
		h.handleGet(w, r, u, filePath)
	case http.MethodPut:
		h.handlePut(w, r, u, urlUserID, filePath)
	case http.MethodDelete:
		h.handleDelete(w, r, u, urlUserID, filePath)
	case "MKCOL":
		h.handleMkcol(w, r, u, urlUserID, filePath)
	case "MOVE":
		h.handleMove(w, r, u, urlUserID, filePath)
	case "COPY":
		h.handleCopy(w, r, u, urlUserID, filePath)
	case "PROPPATCH":
		h.handleProppatch(w, r, u, urlUserID, filePath)
	case "REPORT":
		h.handleReport(w, r, u, urlUserID, filePath)
	case "LOCK":
		h.handleLock(w, r, u, urlUserID, filePath)
	case "UNLOCK":
		h.handleUnlock(w, r, u, urlUserID, filePath)
	case http.MethodOptions:
		w.Header().Set("DAV", davCompliance)
		w.Header().Set("Allow", davAllow)
		// Office edits in place over WebDAV only when told it may.
		w.Header().Set("MS-Author-Via", "DAV")
		w.WriteHeader(http.StatusOK)
	default:
		w.Header().Set("DAV", davCompliance)
		w.Header().Set("Allow", davAllow)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// davAllow is the methods the files tree answers.
const davAllow = "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, MOVE, COPY, REPORT, LOCK, UNLOCK"

// conditions starts checking a request's conditional headers and lock tokens,
// answering 400 when its If header cannot be read.
func (h *WebDAVHandler) conditions(w http.ResponseWriter, r *http.Request, u *user.User) (*davConditions, bool) {
	c, err := newDAVConditions(r, h.Files, u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return c, true
}

// writeError answers the error of a write: a failed condition or a missing
// lock token with its status, a missing file with 404, and anything else as
// a server error, logged as what.
func (h *WebDAVHandler) writeError(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, what string, err error) {
	if writeCondError(w, "/remote.php/dav/files/"+urlUserID, err) {
		return
	}
	if errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.Log.Error(what, "user", u.Username, "path", r.URL.Path, "err", err)
	httperr.Internal(w, r, err)
}

// extractPath strips the WebDAV prefix to get the user ID and file path.
// Path format: /remote.php/dav/files/{user_id}/path/to/file
// The user_id in the URL may be stale (NC clients cache it), so we return
// it separately for use in response hrefs while relying on the authenticated
// user for authorization. urlPath is already decoded, as r.URL.Path is: a
// name holding a literal "%41" stays "%41".
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

// destinationPath returns the file path a MOVE or COPY's Destination names,
// or false when it is not under the files root. The header is a URL, so its
// path is percent-encoded, unlike the request path.
func (h *WebDAVHandler) destinationPath(dest string) (string, bool) {
	if idx := strings.Index(dest, "/remote.php/"); idx >= 0 {
		dest = dest[idx:]
	}
	if decoded, err := url.PathUnescape(dest); err == nil {
		dest = decoded
	}
	if !strings.HasPrefix(dest, "/remote.php/dav/files/") {
		return "", false
	}
	_, p := h.extractPath(dest)
	return p, true
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
	// oc:permissions says what the client may do; on a read-only mount
	// that is read and share, so the client does not offer edits that
	// would fail.
	readOnly, err := h.Files.ReadOnlyChecker(u.Username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	// One query for the locks of the target and everything it lists.
	listing := depth == "1" && rootMeta.IsDir
	locks, err := h.Files.LocksFor(u.Username, filePath, listing)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	now := h.Clock()
	href := davHref(hrefPrefix, filePath, rootMeta.IsDir)
	entry := metaToEntry(href, rootMeta, u.Username, displayName, readOnly(filePath))
	entry.Propstat.Prop.LockDiscovery = lockDiscoveryFor(hrefPrefix, filePath, locks, now)
	entries = append(entries, entry)

	// If depth=1 and target is a directory, list children
	if listing {
		children, err := h.Files.List(u.Username, filePath)
		if err != nil {
			h.Log.Error("PROPFIND list", "user", u.Username, "path", filePath, "err", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for i := range children {
			child := &children[i]
			childHref := davHref(hrefPrefix, child.Path, child.IsDir)
			entry := metaToEntry(childHref, child, u.Username, displayName, readOnly(child.Path))
			entry.Propstat.Prop.LockDiscovery = lockDiscoveryFor(hrefPrefix, child.Path, locks, now)
			entries = append(entries, entry)
		}
	}

	w.Header().Set("DAV", davCompliance)
	writeMultiStatus(h.Log, w, entries)
}

// --- GET ---

func (h *WebDAVHandler) handleGet(w http.ResponseWriter, r *http.Request, u *user.User, filePath string) {
	if filePath == "/" {
		// HEAD on root is a presence check; GET on a directory is not supported.
		if r.Method == http.MethodHead {
			w.Header().Set("DAV", davCompliance)
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
			w.Header().Set("DAV", davCompliance)
			w.WriteHeader(http.StatusOK)
		case m.IsDir:
			w.WriteHeader(http.StatusNotFound)
		case errors.Is(err, fs.ErrNotExist) || errors.Is(err, vfs.ErrNotFound):
			// The metadata outlived the bytes. To the client the file is
			// gone, as the federated endpoint says; the mismatch is ours.
			h.Log.Warn("GET: file has metadata but no content", "user", u.Username, "path", filePath, "err", err)
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
	cogplugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("ETag", `"`+meta.ETag+`"`)

	// A seekable file is served with ranges and conditional requests,
	// which media players and resuming clients depend on; anything else
	// streams whole.
	if rs, ok := f.(io.ReadSeeker); ok {
		http.ServeContent(w, r, meta.Name, time.Unix(meta.MTime, 0).UTC(), rs)
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Last-Modified", time.Unix(meta.MTime, 0).UTC().Format(http.TimeFormat))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(w, f); err != nil {
		h.Log.Error("GET copy error", "err", err)
	}
}

// davHref is the href of p under prefix: a URL, so its path is
// percent-encoded, and a directory's ends in a slash.
func davHref(prefix, p string, isDir bool) string {
	href := prefix + (&url.URL{Path: p}).EscapedPath()
	if isDir && !strings.HasSuffix(href, "/") {
		href += "/"
	}
	return href
}

// --- PUT ---

func (h *WebDAVHandler) handlePut(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot PUT to root", http.StatusBadRequest)
		return
	}
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}

	// The conditions are checked against what the upload replaces, while
	// no other write can change it: If-Match is how a sync client avoids
	// overwriting an edit it has not seen.
	meta, err := h.Files.UploadIf(u.Username, filePath, r.Body, func(cur *files.FileMeta) error {
		scope := lockScope{member: cur == nil}
		if err := c.check(filePath, cur, scope); err != nil {
			return err
		}
		return c.requireTokens(filePath, scope)
	})
	if errors.Is(err, cogplugin.ErrQuotaExceeded) {
		// What clients show as "not enough space" rather than a failure.
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	}
	if err != nil {
		h.writeError(w, r, u, urlUserID, "PUT", err)
		return
	}

	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("OC-FileId", fmt.Sprintf("%08d", meta.ID))
	w.WriteHeader(http.StatusCreated)
}

// --- DELETE ---

func (h *WebDAVHandler) handleDelete(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot delete root", http.StatusForbidden)
		return
	}
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}

	err := h.Files.DeleteIf(u.Username, filePath, func(cur *files.FileMeta) error {
		scope := lockScope{member: true, beneath: true}
		if err := c.check(filePath, cur, scope); err != nil {
			return err
		}
		if cur == nil {
			return files.ErrNotFound
		}
		return c.requireTokens(filePath, scope)
	})
	if err != nil {
		h.writeError(w, r, u, urlUserID, "DELETE", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- MKCOL ---

func (h *WebDAVHandler) handleMkcol(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	if filePath == "/" {
		http.Error(w, "cannot MKCOL root", http.StatusMethodNotAllowed)
		return
	}
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}
	cur, err := h.Files.GetByPath(u.Username, filePath)
	if err != nil && !errors.Is(err, files.ErrNotFound) {
		h.writeError(w, r, u, urlUserID, "MKCOL", err)
		return
	}
	if err != nil {
		cur = nil
	}
	scope := lockScope{member: true}
	if err := c.check(filePath, cur, scope); err != nil {
		h.writeError(w, r, u, urlUserID, "MKCOL", err)
		return
	}
	if err := c.requireTokens(filePath, scope); err != nil {
		h.writeError(w, r, u, urlUserID, "MKCOL", err)
		return
	}

	if _, err := h.Files.CreateDir(u.Username, filePath); err != nil {
		h.Log.Warn("MKCOL", "user", u.Username, "path", filePath, "err", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// --- MOVE ---

func (h *WebDAVHandler) handleMove(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, filePath string) {
	if r.Header.Get("Destination") == "" {
		http.Error(w, "Destination header required", http.StatusBadRequest)
		return
	}
	newPath, ok := h.destinationPath(r.Header.Get("Destination"))
	if !ok {
		http.Error(w, "Destination must be under /remote.php/dav/files/", http.StatusBadGateway)
		return
	}
	if filePath == "/" || newPath == "/" {
		http.Error(w, "the root cannot be moved or replaced", http.StatusForbidden)
		return
	}
	if newPath == filePath || strings.HasPrefix(newPath, filePath+"/") {
		http.Error(w, "a folder cannot be moved into itself", http.StatusForbidden)
		return
	}

	// RFC 4918 §10.6: without Overwrite: F a MOVE replaces what is at the
	// destination; with it, it must not.
	exists, err := h.Files.Exists(u.Username, newPath)
	if err != nil {
		h.Log.Error("MOVE: look up destination", "user", u.Username, "path", newPath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if exists && r.Header.Get("Overwrite") == "F" {
		http.Error(w, "destination exists", http.StatusPreconditionFailed)
		return
	}
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}

	// The conditional headers are about the source, the request's own
	// resource. The source leaves its folder and the destination joins one,
	// and what each covers goes with it, so both need their locks' tokens.
	_, err = h.Files.RenameIf(u.Username, filePath, newPath, func(src, dst *files.FileMeta) error {
		srcScope := lockScope{member: true, beneath: true}
		dstScope := lockScope{member: true, beneath: dst != nil}
		c.also(newPath, dstScope)
		if err := c.check(filePath, src, srcScope); err != nil {
			return err
		}
		if src == nil {
			return files.ErrNotFound
		}
		if err := c.requireTokens(filePath, srcScope); err != nil {
			return err
		}
		return c.requireTokens(newPath, dstScope)
	})
	if writeCondError(w, "/remote.php/dav/files/"+urlUserID, err) {
		return
	}
	if errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Warn("MOVE", "user", u.Username, "from", filePath, "to", newPath, "err", err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if exists {
		w.WriteHeader(http.StatusNoContent) // overwritten
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// --- COPY ---

func (h *WebDAVHandler) handleCopy(w http.ResponseWriter, r *http.Request, u *user.User, urlUserID, srcPath string) {
	if r.Header.Get("Destination") == "" {
		http.Error(w, "Destination header required", http.StatusBadRequest)
		return
	}
	newPath, ok := h.destinationPath(r.Header.Get("Destination"))
	if !ok {
		http.Error(w, "Destination must be under /remote.php/dav/files/", http.StatusBadGateway)
		return
	}
	if newPath == "/" {
		http.Error(w, "the root cannot be replaced", http.StatusForbidden)
		return
	}
	// A copy of a folder into itself would copy its own output until the
	// disk filled.
	if newPath == srcPath || strings.HasPrefix(newPath, srcPath+"/") {
		http.Error(w, "a folder cannot be copied into itself", http.StatusForbidden)
		return
	}

	overwrite := r.Header.Get("Overwrite")
	if overwrite == "" {
		overwrite = "T"
	}

	// Check if destination exists: what is on disk without a row yet is
	// there all the same.
	exists, err := h.Files.Exists(u.Username, newPath)
	if err != nil {
		h.Log.Error("COPY: look up destination", "user", u.Username, "path", newPath, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if exists && overwrite == "F" {
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
	// A copy changes only the destination, so only its locks need tokens.
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}
	dstScope := lockScope{member: true, beneath: exists}
	c.also(newPath, dstScope)
	if err := c.check(srcPath, srcMeta, lockScope{}); err != nil {
		h.writeError(w, r, u, urlUserID, "COPY", err)
		return
	}
	if err := c.requireTokens(newPath, dstScope); err != nil {
		h.writeError(w, r, u, urlUserID, "COPY", err)
		return
	}

	if srcMeta.IsDir {
		err = h.copyDir(u.Username, srcPath, newPath)
	} else {
		err = h.copyFile(u.Username, srcPath, newPath)
	}
	if err != nil {
		h.Log.Error("COPY", "user", u.Username, "from", srcPath, "to", newPath, "err", err)
		httperr.Internal(w, r, err)
		return
	}

	if exists {
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

		_, err = h.Files.GetByID(u.Username, fileID)
		if errors.Is(err, files.ErrNotFound) {
			writeOCSError(w, http.StatusNotFound, 404, "file not found")
			return
		}
		if err != nil {
			h.Log.Error("direct download: look up file", "user", u.Username, "file", fileID, "err", err)
			writeOCSError(w, http.StatusInternalServerError, 500, "look up file failed")
			return
		}

		token, err := h.createDirectToken(u.ID, fileID)
		if err != nil {
			httperr.Log(r, fmt.Errorf("create token: %w", err))
			writeOCSError(w, http.StatusInternalServerError, 500, "internal error")
			return
		}
		url := baseURL + "/remote.php/dav/direct/" + token
		writeOCS(w, http.StatusOK, map[string]any{
			"url": url,
		})
	}
}

func hashDirectToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// createDirectToken mints a link to the user's file. It names the file by
// ID, so a rename before the link is used does not break it; the table
// keeps only the token's hash.
func (h *WebDAVHandler) createDirectToken(userID string, fileID int64) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate direct token: %w", err)
	}
	token := hex.EncodeToString(b)
	now := h.Clock()
	if _, err := h.DB.Exec(`DELETE FROM direct_tokens WHERE created_at <= ?`, now.Add(-directTokenLifetime).Unix()); err != nil {
		return "", fmt.Errorf("prune direct tokens: %w", err)
	}
	if _, err := h.DB.Exec(`INSERT INTO direct_tokens (token_hash, user_id, file_id, created_at) VALUES (?, ?, ?, ?)`,
		hashDirectToken(token), userID, fileID, now.Unix()); err != nil {
		return "", fmt.Errorf("record direct token: %w", err)
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

	// One use: the row goes as it is read.
	var (
		username string
		fileID   int64
	)
	err := h.DB.QueryRow(`DELETE FROM direct_tokens WHERE token_hash = ? AND created_at > ?
		RETURNING (SELECT username FROM users WHERE id = user_id), file_id`,
		hashDirectToken(token), h.Clock().Add(-directTokenLifetime).Unix()).Scan(&username, &fileID)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "invalid or expired token", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Log.Error("direct download: look up token", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	meta, err := h.Files.GetByID(username, fileID)
	if err != nil {
		h.Log.Warn("direct download", "user", username, "file", fileID, "err", err)
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	f, meta, err := h.Files.Download(username, meta.Path)
	if err != nil {
		h.Log.Warn("direct download", "user", username, "path", meta.Path, "err", err)
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	if meta.MimeType != "" {
		w.Header().Set("Content-Type", meta.MimeType)
	}
	cogplugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(meta.Size, 10))
	w.Header().Set("Content-Disposition", httphdr.Attachment(meta.Name))
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
	DisplayName        string         `xml:"D:displayname"`
	ContentType        string         `xml:"D:getcontenttype,omitempty"`
	ContentLength      int64          `xml:"D:getcontentlength"`
	ETag               string         `xml:"D:getetag,omitempty"`
	LastModified       string         `xml:"D:getlastmodified,omitempty"`
	ResourceType       *resourceType  `xml:"D:resourcetype"`
	OCId               string         `xml:"oc:id"`
	OCFileID           string         `xml:"oc:fileid"`
	OCPermissions      string         `xml:"oc:permissions"`
	OCSize             int64          `xml:"oc:size"`
	NCHasPreview       bool           `xml:"nc:has-preview"`
	OCFavorite         int            `xml:"oc:favorite"`
	OCCommentsCount    int            `xml:"oc:comments-count"`
	OCCommentsUnread   int            `xml:"oc:comments-unread"`
	NCMountType        string         `xml:"nc:mount-type"`
	OCOwnerID          string         `xml:"oc:owner-id"`
	OCOwnerDisplayName string         `xml:"oc:owner-display-name"`
	NCSharees          *struct{}      `xml:"nc:sharees"`
	SupportedLock      *supportedLock `xml:"D:supportedlock,omitempty"`
	LockDiscovery      *lockDiscovery `xml:"D:lockdiscovery,omitempty"`
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
	c, ok := h.conditions(w, r, u)
	if !ok {
		return
	}
	cur, err := h.Files.GetByPath(u.Username, filePath)
	if errors.Is(err, files.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		h.writeError(w, r, u, urlUserID, "PROPPATCH", err)
		return
	}
	if err := c.check(filePath, cur, lockScope{}); err != nil {
		h.writeError(w, r, u, urlUserID, "PROPPATCH", err)
		return
	}
	if err := c.requireTokens(filePath, lockScope{}); err != nil {
		h.writeError(w, r, u, urlUserID, "PROPPATCH", err)
		return
	}

	if req.Set.Prop.Favorite != nil {
		fav := *req.Set.Prop.Favorite != 0
		if err := h.Files.SetFavorite(u.Username, filePath, fav); errors.Is(err, files.ErrNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		} else if err != nil {
			h.Log.Error("PROPPATCH favorite", "user", u.Username, "path", filePath, "err", err)
			httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}

	hrefPrefix := "/remote.php/dav/files/" + urlUserID
	displayName := u.DisplayName
	if displayName == "" {
		displayName = u.Username
	}

	readOnly, err := h.Files.ReadOnlyChecker(u.Username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	var entries []propfindEntry
	for i := range favorites {
		f := &favorites[i]
		href := davHref(hrefPrefix, f.Path, f.IsDir)
		entries = append(entries, metaToEntry(href, f, u.Username, displayName, readOnly(f.Path)))
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

func metaToEntry(href string, meta *files.FileMeta, ownerID, ownerDisplayName string, readOnly bool) propfindEntry {
	var rt resourceType
	if meta.IsDir {
		rt.Collection = &struct{}{}
	}

	name := meta.Name
	if meta.Path == "/" {
		name = ""
	}

	permissions := "RGDNVCK" // read, share, delete, rename, move, create, mkdir
	if readOnly {
		permissions = "RG"
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
				SupportedLock:      supportedLocks,
			},
		},
	}
}
