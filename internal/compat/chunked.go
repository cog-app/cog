package compat

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
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
//
// An upload's chunks are the session: they sit in a directory named for
// the user and the transfer under TempDir, so a restart, or another replica
// on the same data directory, picks up where the client left off, and
// nothing in memory has to agree with the disk.
type ChunkedUploadHandler struct {
	Log     *slog.Logger
	Files   *files.Plugin
	TempDir string // base directory for chunks: the data directory
	Clock   clock.Clock
}

// chunkExpiry is how long an upload may sit untouched before it is swept.
const chunkExpiry = 24 * time.Hour

// chunkRoot is where uploads in progress live, by user and transfer.
func (h *ChunkedUploadHandler) chunkRoot() string {
	return filepath.Join(h.TempDir, "cog-chunks")
}

// sessionDir is the directory of one transfer's chunks. The transfer ID is
// the client's, so it is hashed into a name that is always one path element.
func (h *ChunkedUploadHandler) sessionDir(username, transferID string) string {
	sum := sha256.Sum256([]byte(transferID))
	return filepath.Join(h.chunkRoot(), username, hex.EncodeToString(sum[:16]))
}

// staged returns the bytes username's uploads in progress hold.
func (h *ChunkedUploadHandler) staged(username string) (int64, error) {
	var n int64
	err := filepath.WalkDir(filepath.Join(h.chunkRoot(), username), func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil // assembled or cancelled meanwhile
			}
			if err != nil {
				return err
			}
			n += info.Size()
		}
		return nil
	})
	return n, err
}

// sweep removes uploads untouched for chunkExpiry, and the temporary
// directories chunks were kept in before they were named by transfer.
func (h *ChunkedUploadHandler) sweep() {
	cutoff := h.Clock().Add(-chunkExpiry)
	old := func(path string) bool {
		info, err := os.Stat(path)
		return err == nil && info.ModTime().Before(cutoff)
	}
	remove := func(path string) {
		if err := os.RemoveAll(path); err != nil {
			h.Log.Warn("chunked upload: remove stale upload", "dir", path, "err", err)
		}
	}
	legacy, err := filepath.Glob(filepath.Join(h.TempDir, "cog-chunk-*"))
	if err != nil {
		h.Log.Warn("chunked upload: find stale uploads", "err", err)
	}
	for _, d := range legacy {
		if old(d) {
			remove(d)
		}
	}
	users, err := os.ReadDir(h.chunkRoot())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.Log.Warn("chunked upload: find stale uploads", "err", err)
		}
		return
	}
	for _, u := range users {
		transfers, err := os.ReadDir(filepath.Join(h.chunkRoot(), u.Name()))
		if err != nil {
			h.Log.Warn("chunked upload: find stale uploads", "user", u.Name(), "err", err)
			continue
		}
		for _, t := range transfers {
			if d := filepath.Join(h.chunkRoot(), u.Name(), t.Name()); old(d) {
				remove(d)
			}
		}
	}
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

	dir := h.sessionDir(u.Username, transferID)

	switch r.Method {
	case "MKCOL":
		h.handleMkcol(w, dir)
	case http.MethodPut:
		h.handlePut(w, r, u, dir, chunk)
	case "MOVE":
		h.handleMove(w, r, u, dir)
	case http.MethodDelete:
		h.handleDelete(w, dir)
	case "PROPFIND":
		// NC clients may PROPFIND on the upload dir to check status
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		w.WriteHeader(http.StatusMultiStatus)
	default:
		w.Header().Set("DAV", "1, 3, extended-mkcol")
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *ChunkedUploadHandler) handleMkcol(w http.ResponseWriter, dir string) {
	h.sweep()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.Log.Error("chunked upload: create", "dir", dir, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// sessionExists reports whether an upload was started in dir.
func sessionExists(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return info.IsDir(), nil
}

func (h *ChunkedUploadHandler) handlePut(w http.ResponseWriter, r *http.Request, u *user.User, dir, chunk string) {
	ok, err := sessionExists(dir)
	if err != nil {
		h.Log.Error("chunked upload: look up", "dir", dir, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "upload session not found", http.StatusNotFound)
		return
	}

	if chunk == "" || chunk == ".file" {
		http.Error(w, "invalid chunk name", http.StatusBadRequest)
		return
	}
	if chunk != filepath.Base(chunk) {
		http.Error(w, "invalid chunk name", http.StatusBadRequest)
		return
	}

	chunkPath := filepath.Join(dir, chunk)

	// Chunks sit in the data directory until the upload is assembled, and
	// assembling is where the quota is checked; without a check here a
	// client could stage any amount and never assemble it.
	var replaced int64
	if info, err := os.Stat(chunkPath); err == nil {
		replaced = info.Size()
	}
	staged, err := h.staged(u.Username)
	if err != nil {
		h.Log.Error("chunked upload: measure staged chunks", "user", u.Username, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	staged -= replaced
	if r.ContentLength > 0 {
		if err := h.Files.QuotaCheck(u.Username, staged+r.ContentLength); err != nil {
			http.Error(w, err.Error(), http.StatusInsufficientStorage)
			return
		}
	}

	f, err := os.Create(chunkPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	written, err := io.Copy(f, r.Body)
	if err != nil {
		f.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := f.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.Files.QuotaCheck(u.Username, staged+written); err != nil {
		if rmErr := os.Remove(chunkPath); rmErr != nil {
			h.Log.Error("remove over-quota chunk", "path", chunkPath, "err", rmErr)
		}
		http.Error(w, err.Error(), http.StatusInsufficientStorage)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (h *ChunkedUploadHandler) handleMove(w http.ResponseWriter, r *http.Request, u *user.User, dir string) {
	ok, err := sessionExists(dir)
	if err != nil {
		h.Log.Error("chunked upload: look up", "dir", dir, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
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
	if _, after, ok := strings.Cut(dest, "/remote.php/dav/files/"); ok {
		rest := after
		if _, fp, ok := strings.Cut(rest, "/"); ok {
			destPath = "/" + fp
		} else {
			destPath = "/"
		}
	}
	destPath = filepath.Clean(destPath)

	// Assemble chunks in sorted order
	entries, err := os.ReadDir(dir)
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
		f, err := os.Open(filepath.Join(dir, entry.Name()))
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

	if err := os.RemoveAll(dir); err != nil {
		h.Log.Warn("chunked upload session cleanup", "dir", dir, "err", err)
	}

	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	w.Header().Set("OC-FileId", strings.Repeat("0", max(0, 8-len(meta.ETag)))+meta.ETag)
	w.WriteHeader(http.StatusCreated)
}

func (h *ChunkedUploadHandler) handleDelete(w http.ResponseWriter, dir string) {
	ok, err := sessionExists(dir)
	if err != nil {
		h.Log.Error("chunked upload: look up", "dir", dir, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		h.Log.Error("chunked upload: cancel", "dir", dir, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
