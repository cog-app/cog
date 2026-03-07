package files

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

// Pages holds the web UI handlers for the files plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

// filePath extracts and cleans the {path...} wildcard from the request.
func filePath(r *http.Request) string {
	p := r.PathValue("path")
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

func (h *Pages) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, h.plugin.ctx.CSRFSecret())
	}
	var isAdmin bool
	if u != nil {
		isAdmin = u.IsAdmin
	}
	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         buildNav(r.URL.Path, h.plugin.ctx.NavItems()),
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
	}
}

// ServeGet handles GET requests:
//   - ?dl=1 → force download (Content-Disposition: attachment)
//   - ?raw=1 → serve raw file bytes (for <img src>, etc.)
//   - directory → listing page
//   - viewable file → view/editor page
//   - other file → file info page with download link
func (h *Pages) ServeGet(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)

	if err := h.plugin.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := h.plugin.GetByPath(u.Username, fp)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Directory → listing.
	if meta.IsDir {
		items, err := h.plugin.List(u.Username, fp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pd := h.pageData(r, "Files")
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

	// ?pdfpreview=1 → serve office document converted to PDF.
	if r.URL.Query().Get("pdfpreview") == "1" {
		h.serveDocumentPreview(w, r, u.Username, fp, meta)
		return
	}

	// File → view page.
	pd := h.pageData(r, meta.Name)
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

func (h *Pages) serveDownload(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	f, _, err := h.plugin.Download(username, fp)
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

func (h *Pages) serveRaw(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	f, _, err := h.plugin.Download(username, fp)
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

func (h *Pages) servePreview(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	if !HasPreview(meta.MimeType) {
		http.Error(w, "no preview available", http.StatusNotFound)
		return
	}

	size := 256
	if s := r.URL.Query().Get("size"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			size = n
		}
	}

	f, err := h.plugin.Preview(username, fp, size)
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

func (h *Pages) serveDocumentPreview(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	f, err := h.plugin.PreviewPDF(username, fp)
	if err != nil {
		http.Error(w, "preview unavailable: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("ETag", `"`+meta.ETag+`"`)
	http.ServeContent(w, r, meta.Name+".pdf", time.Unix(meta.MTime, 0), f)
}

func (h *Pages) readText(username, fp string) (string, error) {
	f, _, err := h.plugin.Download(username, fp)
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
func (h *Pages) ServePost(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)

	// ?save=1 → save text content.
	if r.URL.Query().Get("save") == "1" {
		content := r.FormValue("content")
		if _, err := h.plugin.Upload(u.Username, fp, strings.NewReader(content)); err != nil {
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
		if _, err := h.plugin.CreateDir(u.Username, dest); err != nil {
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
	if _, err := h.plugin.Upload(u.Username, dest, file); err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
}

// ServeDelete handles DELETE.
func (h *Pages) ServeDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)
	if fp == "/" {
		http.Error(w, "cannot delete root", http.StatusBadRequest)
		return
	}

	if err := h.plugin.Delete(u.Username, fp); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Template helpers ---

func buildNav(currentPath string, items []plugin.NavItem) []ui.NavItem {
	nav := make([]ui.NavItem, len(items))
	for i, item := range items {
		active := currentPath == item.URL || (item.URL != "/" && strings.HasPrefix(currentPath, item.URL))
		nav[i] = ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		}
	}
	return nav
}

// isImageFile returns true if the MIME type is a previewable image.
func isImageFile(mimeType string) bool {
	return HasPreview(mimeType)
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
	if IsOfficeDocument(mimeType) && LibreOfficeAvailable() {
		return "office"
	}
	return "binary"
}

func fileResourceType(isDir bool) string {
	if isDir {
		return "files:directory"
	}
	return "files:file"
}

// --- Flash helpers (cookie-based one-time messages) ---

const flashCookieName = "cog_flash"

type flash struct {
	Kind    string `json:"k"`
	Message string `json:"m"`
}

func setFlash(w http.ResponseWriter, kind, message string) {
	data, _ := json.Marshal(flash{Kind: kind, Message: message})
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    url.QueryEscape(string(data)),
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func getFlash(w http.ResponseWriter, r *http.Request) *flash {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	val, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return nil
	}
	var f flash
	if err := json.Unmarshal([]byte(val), &f); err != nil {
		return nil
	}
	return &f
}

// --- Path helpers ---

type breadcrumbSeg struct {
	Name string
	Path string
}

func breadcrumbSegments(dirPath string) []breadcrumbSeg {
	cleaned := path.Clean("/" + dirPath)
	if cleaned == "/" {
		return nil
	}
	parts := splitPath(cleaned)
	var segs []breadcrumbSeg
	for i, p := range parts {
		segs = append(segs, breadcrumbSeg{
			Name: p,
			Path: "/" + path.Join(parts[:i+1]...),
		})
	}
	return segs
}

func splitPath(p string) []string {
	var parts []string
	clean := path.Clean(p)
	for clean != "/" && clean != "." && clean != "" {
		parts = append([]string{path.Base(clean)}, parts...)
		clean = path.Dir(clean)
	}
	return parts
}

func parentDir(p string) string {
	parent := path.Dir(path.Clean("/" + p))
	if parent == "." {
		return "/"
	}
	return parent
}

func safeDomID(p string) string {
	var out []byte
	for _, c := range []byte(p) {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			out = append(out, c)
		} else {
			out = append(out, '_')
		}
	}
	return string(out)
}
