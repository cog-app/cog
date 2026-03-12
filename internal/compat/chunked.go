package compat

import (
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// ChunkedUploadHandler handles NC v2 chunked uploads.
//
// Protocol:
//  1. MKCOL /remote.php/dav/uploads/{userId}/{transferId}/ — create upload session
//  2. PUT /remote.php/dav/uploads/{userId}/{transferId}/{chunkNum} — upload chunk
//  3. MOVE /remote.php/dav/uploads/{userId}/{transferId}/.file — assemble to destination
//  4. DELETE /remote.php/dav/uploads/{userId}/{transferId}/ — cancel upload
type ChunkedUploadHandler struct {
	Log     *slog.Logger
	Files   *files.Plugin
	TempDir string // base temp directory for chunks

	mu       sync.Mutex
	sessions map[string]*uploadSession
}

type uploadSession struct {
	Username  string
	Dir       string // temp directory holding chunks
	CreatedAt time.Time
}

func (h *ChunkedUploadHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Path: /remote.php/dav/uploads/{userId}/{transferId}[/{chunk}]
	rest := strings.TrimPrefix(r.URL.Path, "/remote.php/dav/uploads/")
	parts := strings.SplitN(rest, "/", 3)
	if len(parts) < 2 {
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		if r.Method == "PROPFIND" || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}

	// parts[0] = userId, parts[1] = transferId, parts[2] = chunk (optional)
	transferID := parts[1]
	chunk := ""
	if len(parts) > 2 {
		chunk = parts[2]
	}

	sessionKey := u.Username + "/" + transferID

	switch r.Method {
	case "MKCOL":
		h.handleMkcol(w, u, sessionKey)
	case http.MethodPut:
		h.handlePut(w, r, u, sessionKey, chunk)
	case "MOVE":
		h.handleMove(w, r, u, sessionKey)
	case http.MethodDelete:
		h.handleDelete(w, u, sessionKey)
	case "PROPFIND":
		// NC clients may PROPFIND on the upload dir to check status
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		w.WriteHeader(http.StatusMultiStatus)
	default:
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *ChunkedUploadHandler) handleMkcol(w http.ResponseWriter, u *user.User, sessionKey string) {
	dir, err := os.MkdirTemp(h.TempDir, "cog-chunk-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.mu.Lock()
	if h.sessions == nil {
		h.sessions = make(map[string]*uploadSession)
	}
	// Evict stale sessions (older than 24h)
	for k, s := range h.sessions {
		if time.Since(s.CreatedAt) > 24*time.Hour {
			os.RemoveAll(s.Dir)
			delete(h.sessions, k)
		}
	}
	h.sessions[sessionKey] = &uploadSession{
		Username:  u.Username,
		Dir:       dir,
		CreatedAt: time.Now(),
	}
	h.mu.Unlock()

	w.WriteHeader(http.StatusCreated)
}

func (h *ChunkedUploadHandler) handlePut(w http.ResponseWriter, r *http.Request, u *user.User, sessionKey, chunk string) {
	h.mu.Lock()
	sess := h.sessions[sessionKey]
	h.mu.Unlock()

	if sess == nil || sess.Username != u.Username {
		http.Error(w, "upload session not found", http.StatusNotFound)
		return
	}

	if chunk == "" || chunk == ".file" {
		http.Error(w, "invalid chunk name", http.StatusBadRequest)
		return
	}

	chunkPath := filepath.Join(sess.Dir, chunk)
	f, err := os.Create(chunkPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(f, r.Body); err != nil {
		f.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
}

func (h *ChunkedUploadHandler) handleMove(w http.ResponseWriter, r *http.Request, u *user.User, sessionKey string) {
	h.mu.Lock()
	sess := h.sessions[sessionKey]
	h.mu.Unlock()

	if sess == nil || sess.Username != u.Username {
		http.Error(w, "upload session not found", http.StatusNotFound)
		return
	}

	// Destination header contains the target file path
	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "missing Destination header", http.StatusBadRequest)
		return
	}

	// Extract the file path from the Destination URL
	destPath := dest
	if idx := strings.Index(dest, "/remote.php/dav/files/"); idx >= 0 {
		rest := dest[idx+len("/remote.php/dav/files/"):]
		if _, fp, ok := strings.Cut(rest, "/"); ok {
			destPath = "/" + fp
		} else {
			destPath = "/"
		}
	}
	destPath = filepath.Clean(destPath)

	// Assemble chunks in sorted order
	entries, err := os.ReadDir(sess.Dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Create a concatenated reader
	readers := make([]io.Reader, 0, len(entries))
	closers := make([]io.Closer, 0, len(entries))
	defer func() {
		for _, c := range closers {
			c.Close()
		}
	}()

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		f, err := os.Open(filepath.Join(sess.Dir, entry.Name()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		readers = append(readers, f)
		closers = append(closers, f)
	}

	if len(readers) == 0 {
		http.Error(w, "no chunks found", http.StatusBadRequest)
		return
	}

	combined := io.MultiReader(readers...)

	if err := h.Files.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := h.Files.Upload(u.Username, destPath, combined)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Clean up session
	h.mu.Lock()
	delete(h.sessions, sessionKey)
	h.mu.Unlock()
	os.RemoveAll(sess.Dir)

	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("OC-FileId", strings.Repeat("0", max(0, 8-len(meta.ETag)))+meta.ETag)
	w.WriteHeader(http.StatusCreated)
}

func (h *ChunkedUploadHandler) handleDelete(w http.ResponseWriter, u *user.User, sessionKey string) {
	h.mu.Lock()
	sess := h.sessions[sessionKey]
	if sess != nil && sess.Username == u.Username {
		delete(h.sessions, sessionKey)
	}
	h.mu.Unlock()

	if sess == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	os.RemoveAll(sess.Dir)
	w.WriteHeader(http.StatusNoContent)
}
