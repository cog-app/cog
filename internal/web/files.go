package web

import (
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/services"
)

// FilesHandler serves the files browser UI at /apps/files/.
type FilesHandler struct{ p *services.Providers }

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

	if err := h.p.Files.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := h.p.Files.GetByPath(u.Username, fp)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Directory → listing.
	if meta.IsDir {
		items, err := h.p.Files.List(u.Username, fp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pd := pageDataWithNav(r, "Files", h.p.CSRFSecret, h.p.Plugins)
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

	// ?preview=1 → serve resized JPEG thumbnail.
	if r.URL.Query().Get("preview") == "1" {
		h.servePreview(w, r, u.Username, fp, meta)
		return
	}

	// File → view page.
	pd := pageDataWithNav(r, meta.Name, h.p.CSRFSecret, h.p.Plugins)
	kind := fileViewKind(meta.MimeType)

	var textContent string
	if kind == "text" {
		if content, err := h.readText(u.Username, fp); err == nil {
			textContent = content
		}
	}

	var flashMsg, flashKind string
	if flash := getFlash(w, r); flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}

	fileViewPage(pd, fp, meta, kind, textContent, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *FilesHandler) serveDownload(w http.ResponseWriter, r *http.Request, username, fp string, meta *files.FileMeta) {
	f, _, err := h.p.Files.Download(username, fp)
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
	f, _, err := h.p.Files.Download(username, fp)
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

func (h *FilesHandler) servePreview(w http.ResponseWriter, r *http.Request, username, fp string, meta *files.FileMeta) {
	if !files.HasPreview(meta.MimeType) {
		http.Error(w, "no preview available", http.StatusNotFound)
		return
	}

	size := 256
	if s := r.URL.Query().Get("size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			size = n
		}
	}

	f, err := h.p.Files.Preview(username, fp, size)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	io.Copy(w, f)
}

func (h *FilesHandler) readText(username, fp string) (string, error) {
	f, _, err := h.p.Files.Download(username, fp)
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

// ServePost handles POST — upload file, create directory, or save text content.
func (h *FilesHandler) ServePost(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)

	// ?save=1 → save text content.
	if r.URL.Query().Get("save") == "1" {
		content := r.FormValue("content")
		if _, err := h.p.Files.Upload(u.Username, fp, strings.NewReader(content)); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		setFlash(w, "success", "File saved.")
		http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
		return
	}

	// Try mkdir (simple form field).
	name := r.FormValue("name")
	if name != "" {
		dest := path.Join(fp, name)
		if _, err := h.p.Files.CreateDir(u.Username, dest); err != nil {
			http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
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

	dest := path.Join(fp, header.Filename)
	if _, err := h.p.Files.Upload(u.Username, dest, file); err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
}

// ServeDelete handles DELETE.
func (h *FilesHandler) ServeDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)
	if fp == "/" {
		http.Error(w, "cannot delete root", http.StatusBadRequest)
		return
	}

	if err := h.p.Files.Delete(u.Username, fp); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// fileIconName returns the icon category for a file.
func fileIconName(mimeType string, isDir bool) string {
	if isDir {
		return "folder"
	}
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if strings.HasPrefix(mimeType, "text/") {
		return "text"
	}
	if strings.HasPrefix(mimeType, "audio/") {
		return "audio"
	}
	if strings.HasPrefix(mimeType, "video/") {
		return "video"
	}
	switch mimeType {
	case "application/json", "application/xml", "application/javascript",
		"application/x-yaml", "application/toml", "application/x-sh",
		"application/sql", "application/x-httpd-php":
		return "text"
	}
	return "file"
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
