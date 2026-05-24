package files

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
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
	nav := buildNav(r.URL.Path, h.plugin.ctx.NavItems())
	var favicon string
	for _, item := range nav {
		if item.Active {
			favicon = ui.IconToFavicon(item.Icon)
			break
		}
	}
	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         nav,
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
		PluginID:    "files",
		Favicon:     favicon,
	}
}

// ServeVolumes shows accessible storage volumes.
func (h *Pages) ServeVolumes(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	va := h.plugin.ctx.VolumeAccess()
	if va == nil {
		http.Error(w, "volumes not available", http.StatusInternalServerError)
		return
	}

	volumes, err := va.ListAccessible(u.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pd := h.pageData(r, "Volumes")
	volumesPage(pd, volumes).Render(r.Context(), w)
}

// ServeFavorites shows all favorited files.
func (h *Pages) ServeFavorites(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	if err := h.plugin.EnsureRootDir(u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	files, err := h.plugin.Favorites(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	iconMap := h.buildIconMap(u.Username, files)
	pd := h.pageData(r, "Favorites")
	favoritesPage(pd, files, iconMap).Render(r.Context(), w)
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
		// Path not found — try archive boundary detection.
		archiveMeta, archivePath, innerPath := h.resolveArchivePath(u.Username, fp)
		if archiveMeta != nil {
			h.serveArchiveContent(w, r, u.Username, archiveMeta, archivePath, innerPath)
			return
		}
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
		iconMap := h.buildIconMap(u.Username, items)
		pd := h.pageData(r, "Files")
		filesPage(pd, fp, items, iconMap).Render(r.Context(), w)
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

	// Archive file → show archive listing.
	if isArchiveFile(meta) {
		h.serveArchiveListing(w, r, u.Username, meta, fp, "")
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
	if flash := h.getFlash(w, r); flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}

	fileViewPage(pd, fp, meta, kind, textContent, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *Pages) serveDownload(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	rc, _, err := h.plugin.Download(username, fp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer rc.Close()

	ct := meta.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+meta.Name+"\"")
	serveContent(w, r, meta.Name, time.Unix(meta.MTime, 0), rc)
}

func (h *Pages) serveRaw(w http.ResponseWriter, r *http.Request, username, fp string, meta *FileMeta) {
	rc, _, err := h.plugin.Download(username, fp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer rc.Close()

	ct := meta.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	serveContent(w, r, meta.Name, time.Unix(meta.MTime, 0), rc)
}

// serveContent serves an io.ReadCloser via http.ServeContent. If the reader
// implements io.ReadSeeker (e.g. *os.File), it is used directly; otherwise
// the content is buffered into a bytes.ReadSeeker.
func serveContent(w http.ResponseWriter, r *http.Request, name string, modtime time.Time, rc io.ReadCloser) {
	if rs, ok := rc.(io.ReadSeeker); ok {
		http.ServeContent(w, r, name, modtime, rs)
		return
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, name, modtime, bytes.NewReader(data))
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

// --- Archive handlers ---

// resolveArchivePath walks up the path looking for an archive file boundary.
// Returns the archive metadata, archive path, and inner path (or nil if not found).
func (h *Pages) resolveArchivePath(username, fullPath string) (*FileMeta, string, string) {
	parts := splitPath(fullPath)
	for i := 1; i <= len(parts); i++ {
		candidate := "/" + path.Join(parts[:i]...)
		meta, err := h.plugin.GetByPath(username, candidate)
		if err != nil {
			continue
		}
		if !meta.IsDir && isArchiveFile(meta) && i < len(parts) {
			innerPath := path.Join(parts[i:]...)
			return meta, candidate, innerPath
		}
	}
	return nil, "", ""
}

// openArchiveSeeker returns a seekable+readable handle to an archive file.
func (h *Pages) openArchiveSeeker(username, archivePath string, meta *FileMeta) (archiveReader, io.Closer, error) {
	rc, _, err := h.plugin.Download(username, archivePath)
	if err != nil {
		return nil, nil, err
	}
	// Local files are *os.File which implements both io.ReadSeeker and io.ReaderAt.
	if f, ok := rc.(*os.File); ok {
		return f, f, nil
	}
	// Non-local: buffer into memory (with size limit).
	const maxBufferSize = 500 << 20 // 500MB
	if meta.Size > maxBufferSize {
		rc.Close()
		return nil, nil, fmt.Errorf("archive too large for non-local backend (%s)", formatSize(meta.Size))
	}
	data, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		return nil, nil, err
	}
	return bytes.NewReader(data), io.NopCloser(nil), nil
}

// serveArchiveListing lists entries at innerPath within the archive and renders the template.
func (h *Pages) serveArchiveListing(w http.ResponseWriter, r *http.Request, username string, meta *FileMeta, archivePath, innerPath string) {
	ar, closer, err := h.openArchiveSeeker(username, archivePath, meta)
	if err != nil {
		http.Error(w, "open archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer closer.Close()

	format := ArchiveFormat(meta.Name)
	entries, err := ListArchive(ar, meta.Size, format, innerPath)
	if err != nil {
		http.Error(w, "list archive: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var flashMsg, flashKind string
	if flash := h.getFlash(w, r); flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}

	pd := h.pageData(r, meta.Name)
	archivePage(pd, archivePath, innerPath, meta, entries, flashMsg, flashKind).Render(r.Context(), w)
}

// serveArchiveContent handles requests for paths inside an archive.
func (h *Pages) serveArchiveContent(w http.ResponseWriter, r *http.Request, username string, meta *FileMeta, archivePath, innerPath string) {
	q := r.URL.Query()

	// ?dl=1 or ?raw=1 → serve file content from archive.
	if q.Get("dl") == "1" || q.Get("raw") == "1" {
		h.serveArchiveFileStream(w, r, username, meta, archivePath, innerPath, q.Get("dl") == "1")
		return
	}

	// Determine if innerPath is a directory or file within the archive.
	ar, closer, err := h.openArchiveSeeker(username, archivePath, meta)
	if err != nil {
		http.Error(w, "open archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer closer.Close()

	format := ArchiveFormat(meta.Name)

	// Check if it's a directory (has children).
	if isArchiveEntryDir(ar, meta.Size, format, innerPath) {
		// Reset reader position for listing.
		if _, err := ar.Seek(0, io.SeekStart); err != nil {
			http.Error(w, "seek: "+err.Error(), http.StatusInternalServerError)
			return
		}
		entries, err := ListArchive(ar, meta.Size, format, innerPath)
		if err != nil {
			http.Error(w, "list archive: "+err.Error(), http.StatusInternalServerError)
			return
		}
		pd := h.pageData(r, meta.Name)
		archivePage(pd, archivePath, innerPath, meta, entries, "", "").Render(r.Context(), w)
		return
	}

	// It's a file — show view page.
	if _, err := ar.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "seek: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rc, entry, err := ReadArchiveFile(ar, meta.Size, format, innerPath)
	if err != nil {
		http.Error(w, "not found in archive", http.StatusNotFound)
		return
	}

	kind := archiveViewKind(entry.MimeType)
	var textContent string
	if kind == "text" {
		limited := io.LimitReader(rc, 1<<20)
		b, err := io.ReadAll(limited)
		rc.Close()
		if err == nil {
			textContent = string(b)
		}
	} else {
		rc.Close()
	}

	pd := h.pageData(r, entry.Name)
	archiveEntryViewPage(pd, archivePath, entry, kind, textContent).Render(r.Context(), w)
}

// serveArchiveFileStream extracts and streams a single file from an archive.
func (h *Pages) serveArchiveFileStream(w http.ResponseWriter, r *http.Request, username string, meta *FileMeta, archivePath, innerPath string, download bool) {
	ar, closer, err := h.openArchiveSeeker(username, archivePath, meta)
	if err != nil {
		http.Error(w, "open archive: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer closer.Close()

	format := ArchiveFormat(meta.Name)
	rc, entry, err := ReadArchiveFile(ar, meta.Size, format, innerPath)
	if err != nil {
		http.Error(w, "not found in archive", http.StatusNotFound)
		return
	}
	defer rc.Close()

	ct := entry.MimeType
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	if download {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+entry.Name+"\"")
	}
	// Buffer for range request support.
	data, err := io.ReadAll(rc)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeContent(w, r, entry.Name, time.Unix(entry.MTime, 0), bytes.NewReader(data))
}

// handleExtract extracts an archive to the filesystem.
func (h *Pages) handleExtract(w http.ResponseWriter, r *http.Request, username, fp string) {
	meta, err := h.plugin.GetByPath(username, fp)
	if err != nil {
		h.setFlash(w, "error", "File not found.")
		http.Redirect(w, r, "/apps/files"+parentDir(fp), http.StatusSeeOther)
		return
	}
	if !isArchiveFile(meta) {
		h.setFlash(w, "error", "Not an archive file.")
		http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
		return
	}

	destDir := ExtractDestination(fp)
	count, err := h.plugin.ExtractArchive(username, fp, destDir)
	if err != nil {
		h.setFlash(w, "error", "Extract failed: "+err.Error())
		http.Redirect(w, r, "/apps/files"+fp, http.StatusSeeOther)
		return
	}

	h.setFlash(w, "success", fmt.Sprintf("Extracted %d items to %s", count, path.Base(destDir)))
	http.Redirect(w, r, "/apps/files"+destDir, http.StatusSeeOther)
}

// archiveViewKind categorizes a MIME type for the archive entry view page.
// Simpler than fileViewKind — no office/preview support inside archives.
func archiveViewKind(mimeType string) string {
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
	return "binary"
}

// ServePost handles POST — upload file, create directory, save text content, or extract archive.
func (h *Pages) ServePost(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)

	// ?extract=1 → extract archive.
	if r.URL.Query().Get("extract") == "1" {
		h.handleExtract(w, r, u.Username, fp)
		return
	}

	// ?save=1 → save text content.
	if r.URL.Query().Get("save") == "1" {
		content := r.FormValue("content")
		if _, err := h.plugin.Upload(u.Username, fp, strings.NewReader(content)); err != nil {
			http.Error(w, "save failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		h.setFlash(w, "success", "File saved.")
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

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}

	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			http.Error(w, "open failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		dest := path.Join(fp, header.Filename)
		_, uploadErr := h.plugin.Upload(u.Username, dest, file)
		file.Close()
		if uploadErr != nil {
			http.Error(w, "upload failed: "+uploadErr.Error(), http.StatusInternalServerError)
			return
		}
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

// buildIconMap resolves custom folder icons for directory entries.
func (h *Pages) buildIconMap(username string, items []FileMeta) map[string]string {
	m := map[string]string{}
	for _, f := range items {
		if f.IsDir {
			if icon := h.plugin.folderIcon(username, strings.TrimPrefix(f.Path, "/")); icon != "" {
				m[f.Path] = icon
			}
		}
	}
	return m
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
	if isArchiveMIME(mimeType) {
		return "archive"
	}
	return "file"
}

// fileViewKind categorizes a MIME type for the view page.
func fileViewKind(mimeType string) string {
	if strings.HasPrefix(mimeType, "image/") {
		return "image"
	}
	if HasPreview(mimeType) {
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

// imageViewURL returns the URL for displaying an image on the view page.
// Native images use raw serving; converted formats use a large preview.
func imageViewURL(fp, mimeType string) string {
	if isNativeImage(mimeType) {
		return "/apps/files" + fp + "?raw=1"
	}
	return "/apps/files" + fp + "?preview=1&size=1024"
}

func fileResourceType(isDir bool) string {
	if isDir {
		return "directory"
	}
	return "file"
}

// --- Flash helpers (cookie-based one-time messages) ---

const flashCookieName = "cog_flash"

type flash struct {
	Kind    string `json:"k"`
	Message string `json:"m"`
}

func (h *Pages) setFlash(w http.ResponseWriter, kind, message string) {
	data, _ := json.Marshal(flash{Kind: kind, Message: message})
	h.plugin.ctx.Cookies().Set(w, &http.Cookie{
		Name:   flashCookieName,
		Value:  url.QueryEscape(string(data)),
		MaxAge: 60,
	})
}

func (h *Pages) getFlash(w http.ResponseWriter, r *http.Request) *flash {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	h.plugin.ctx.Cookies().Clear(w, flashCookieName)
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

func starClass(active bool) string {
	return ui.StarClass(active)
}

// archiveInnerSegments returns breadcrumb segments for paths inside an archive.
func archiveInnerSegments(archivePath, innerPath string) []breadcrumbSeg {
	if innerPath == "" {
		return nil
	}
	parts := splitPath(innerPath)
	var segs []breadcrumbSeg
	for i, p := range parts {
		segs = append(segs, breadcrumbSeg{
			Name: p,
			Path: path.Join(parts[:i+1]...),
		})
	}
	return segs
}

// archiveParentURL returns the URL for navigating up one level inside an archive.
func archiveParentURL(archivePath, innerPath string) string {
	if innerPath == "" {
		return "/apps/files" + parentDir(archivePath)
	}
	parent := path.Dir(innerPath)
	if parent == "." || parent == "/" {
		return "/apps/files" + archivePath
	}
	return "/apps/files" + archivePath + "/" + parent
}

// archiveBaseName returns the filename of the archive for breadcrumbs.
func archiveBaseName(archivePath string) string {
	return path.Base(archivePath)
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
