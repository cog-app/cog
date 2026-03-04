package web

import (
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
)

// FilesHandler serves the files browser UI at /apps/files/.
type FilesHandler struct {
	Files    *files.Plugin
	Registry *plugin.Registry
	Secret   []byte
}

// filePath extracts and cleans the {path...} wildcard from the request.
func filePath(r *http.Request) string {
	p := r.PathValue("path")
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

// ServeGet handles GET requests:
//   - ?dl=1 → force download (Content-Disposition: attachment)
//   - ?raw=1 → serve raw file bytes (for <img src>, etc.)
//   - directory → listing page
//   - viewable file → view/editor page
//   - other file → file info page with download link
func (h *FilesHandler) ServeGet(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)

	if err := h.Files.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := h.Files.GetByPath(u.Username, fp)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Directory → listing.
	if meta.IsDir {
		items, err := h.Files.List(u.Username, fp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pd := pageDataWithNav(r, "Files", h.Secret, h.Registry)
		filesPage(pd, fp, items).Render(r.Context(), w)
		return
	}

	// ?dl=1 → force download.
	if r.URL.Query().Get("dl") == "1" {
		h.serveDownload(w, r, u.Username, fp, meta)
		return
	}

	// ?raw=1 → serve raw content (for img src, iframe, etc.).
	if r.URL.Query().Get("raw") == "1" {
		h.serveRaw(w, r, u.Username, fp, meta)
		return
	}

	// File → view page.
	pd := pageDataWithNav(r, meta.Name, h.Secret, h.Registry)
	kind := fileViewKind(meta.MimeType)

	var textContent string
	if kind == "text" {
		if content, err := h.readText(u.Username, fp); err == nil {
			textContent = content
		}
	}

	fileViewPage(pd, fp, meta, kind, textContent).Render(r.Context(), w)
}

func (h *FilesHandler) serveDownload(w http.ResponseWriter, r *http.Request, username, fp string, meta *files.FileMeta) {
	f, _, err := h.Files.Download(username, fp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	ct := meta.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+meta.Name+"\"")
	http.ServeContent(w, r, meta.Name, time.Unix(meta.MTime, 0), f)
}

func (h *FilesHandler) serveRaw(w http.ResponseWriter, r *http.Request, username, fp string, meta *files.FileMeta) {
	f, _, err := h.Files.Download(username, fp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	ct := meta.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	http.ServeContent(w, r, meta.Name, time.Unix(meta.MTime, 0), f)
}

func (h *FilesHandler) readText(username, fp string) (string, error) {
	f, _, err := h.Files.Download(username, fp)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Cap at 1MB for the text editor.
	limited := io.LimitReader(f, 1<<20)
	b, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ServePost handles POST — upload file or create directory.
// If form field "name" is set, it's a mkdir. Otherwise it's a file upload.
func (h *FilesHandler) ServePost(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	dirPath := filePath(r)

	// Try mkdir first (simple form field).
	name := r.FormValue("name")
	if name != "" {
		dest := path.Join(dirPath, name)
		if _, err := h.Files.CreateDir(u.Username, dest); err != nil {
			http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/apps/files"+dirPath, http.StatusSeeOther)
		return
	}

	// Otherwise, file upload.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dest := path.Join(dirPath, header.Filename)
	if _, err := h.Files.Upload(u.Username, dest, file); err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/files"+dirPath, http.StatusSeeOther)
}

// ServeDelete handles DELETE.
func (h *FilesHandler) ServeDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)
	if fp == "/" {
		http.Error(w, "cannot delete root", http.StatusBadRequest)
		return
	}

	if err := h.Files.Delete(u.Username, fp); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// fileViewKind categorizes a MIME type for the view page.
func fileViewKind(mimeType string) string {
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if strings.HasPrefix(mimeType, "text/") {
		return "text"
	}
	// Common text-like types that don't start with text/.
	switch mimeType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/toml", "application/x-sh",
		"application/sql", "application/x-httpd-php":
		return "text"
	}
	if strings.HasPrefix(mimeType, "video/") {
		return "video"
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return "audio"
	}
	if mimeType == "application/pdf" {
		return "pdf"
	}
	return "binary"
}
