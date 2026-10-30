package files

import (
	"bytes"
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

// pageData builds the shell's data for a page. It takes the flash a redirect
// left behind, so the layout renders it above the page.
func (h *Pages) pageData(w http.ResponseWriter, r *http.Request, title string) ui.PageData {
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
		Flash:       h.plugin.ctx.Cookies().TakeFlash(w, r),
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

	pd := h.pageData(w, r, "Volumes")
	ui.Render(w, r, volumesPage(pd, volumes))
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
	pd := h.pageData(w, r, "Favorites")
	ui.Render(w, r, favoritesPage(pd, files, iconMap, h.plugin.ctx.Now()))
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
		pd := h.pageData(w, r, dirTitle(fp))
		ui.Render(w, r, filesPage(pd, fp, items, iconMap, h.plugin.ctx.Now()))
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
	pd := h.pageData(w, r, meta.Name)
	kind := fileViewKind(meta.MimeType)

	var textContent string
	if kind == "text" {
		content, err := h.readText(u.Username, fp)
		if err != nil {
			http.Error(w, "read file: "+err.Error(), http.StatusInternalServerError)
			return
		}
		textContent = content
	}

	var nav imageNav
	if kind == "image" {
		siblings, err := h.plugin.List(u.Username, parentDir(fp))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		nav = imageNeighbours(siblings, fp)
	}

	ui.Render(w, r, fileViewPage(pd, fp, meta, kind, textContent, nav, h.plugin.ctx.Now()))
}

// imageNav places an image among the images of its folder, for the
// previous/next links on its view page.
type imageNav struct {
	Prev, Next string // paths; "" at either end
	Index      int    // 1-based position
	Total      int
}

// imageNeighbours finds the images either side of fp in a folder listing,
// which is already in display order.
func imageNeighbours(items []FileMeta, fp string) imageNav {
	var images []string
	for _, f := range items {
		if !f.IsDir && fileViewKind(f.MimeType) == "image" {
			images = append(images, f.Path)
		}
	}
	var nav imageNav
	for i, p := range images {
		if p != fp {
			continue
		}
		nav.Index, nav.Total = i+1, len(images)
		if i > 0 {
			nav.Prev = images[i-1]
		}
		if i < len(images)-1 {
			nav.Next = images[i+1]
		}
	}
	return nav
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

	pd := h.pageData(w, r, meta.Name)
	ui.Render(w, r, archivePage(pd, archivePath, innerPath, entries, h.plugin.ctx.Now()))
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
		pd := h.pageData(w, r, path.Base(innerPath))
		ui.Render(w, r, archivePage(pd, archivePath, innerPath, entries, h.plugin.ctx.Now()))
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
		b, err := io.ReadAll(io.LimitReader(rc, 1<<20))
		rc.Close()
		if err != nil {
			http.Error(w, "read archive entry: "+err.Error(), http.StatusInternalServerError)
			return
		}
		textContent = string(b)
	} else {
		rc.Close()
	}

	pd := h.pageData(w, r, entry.Name)
	ui.Render(w, r, archiveEntryViewPage(pd, archivePath, entry, kind, textContent))
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
		h.flashRedirect(w, r, "error", "File not found.", fileURL(parentDir(fp)))
		return
	}
	if !isArchiveFile(meta) {
		h.flashRedirect(w, r, "error", "Not an archive file.", fileURL(fp))
		return
	}

	destDir := ExtractDestination(fp)
	count, err := h.plugin.ExtractArchive(username, fp, destDir)
	if err != nil {
		h.flashRedirect(w, r, "error", "Extract failed: "+err.Error(), fileURL(fp))
		return
	}

	h.flashRedirect(w, r, "success", fmt.Sprintf("Extracted %d items to “%s”.", count, path.Base(destDir)), fileURL(destDir))
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

// ServePost handles the forms the file browser posts. Each one redirects
// back with a flash message (Post-Redirect-Get), except ?star=1, which an
// htmx button calls for its own replacement.
//
//   - ?extract=1 extracts the archive at the path
//   - ?save=1 saves the text editor's content to the file
//   - ?rename=1 renames the file or folder at the path to the form's name
//   - ?delete=1 moves the form's paths to the trash; the path is the folder to return to
//   - ?star=1 toggles the favourite and returns the new star button
//   - a name field creates a folder in the path's folder
//   - otherwise the files in the form are uploaded into the path's folder
func (h *Pages) ServePost(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fp := filePath(r)
	q := r.URL.Query()

	switch {
	case q.Get("extract") == "1":
		h.handleExtract(w, r, u.Username, fp)
	case q.Get("save") == "1":
		h.handleSave(w, r, u.Username, fp)
	case q.Get("rename") == "1":
		h.handleRename(w, r, u.Username, fp)
	case q.Get("delete") == "1":
		h.handleDeleteForm(w, r, u.Username, fp)
	case q.Get("star") == "1":
		h.handleStar(w, r, u.Username, fp)
	case r.FormValue("name") != "":
		h.handleMkdir(w, r, u.Username, fp)
	default:
		h.handleUpload(w, r, u.Username, fp)
	}
}

// flashRedirect leaves a message for the page at target and sends the
// browser there.
func (h *Pages) flashRedirect(w http.ResponseWriter, r *http.Request, kind, msg, target string) {
	h.plugin.ctx.Cookies().SetFlash(w, plugin.Flash{Kind: kind, Message: msg})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (h *Pages) handleSave(w http.ResponseWriter, r *http.Request, username, fp string) {
	if _, err := h.plugin.Upload(username, fp, strings.NewReader(r.FormValue("content"))); err != nil {
		h.flashRedirect(w, r, "error", "The file could not be saved: "+err.Error(), fileURL(fp))
		return
	}
	h.flashRedirect(w, r, "success", "Saved "+path.Base(fp)+".", fileURL(fp))
}

// validName rejects a name that would not stay inside its folder.
func validName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a name is required")
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a valid name", name)
	case strings.ContainsAny(name, "/\\"):
		return fmt.Errorf("a name cannot contain a slash")
	}
	return nil
}

func (h *Pages) handleMkdir(w http.ResponseWriter, r *http.Request, username, fp string) {
	name := strings.TrimSpace(r.FormValue("name"))
	if err := validName(name); err != nil {
		h.flashRedirect(w, r, "error", "The folder could not be created: "+err.Error()+".", fileURL(fp))
		return
	}
	if _, err := h.plugin.CreateDir(username, path.Join(fp, name)); err != nil {
		h.flashRedirect(w, r, "error", "The folder could not be created: "+err.Error(), fileURL(fp))
		return
	}
	h.flashRedirect(w, r, "success", "Created “"+name+"”.", fileURL(fp))
}

func (h *Pages) handleRename(w http.ResponseWriter, r *http.Request, username, fp string) {
	back := fileURL(parentDir(fp))
	name := strings.TrimSpace(r.FormValue("name"))
	if err := validName(name); err != nil {
		h.flashRedirect(w, r, "error", "“"+path.Base(fp)+"” could not be renamed: "+err.Error()+".", back)
		return
	}
	if fp == "/" {
		h.flashRedirect(w, r, "error", "The root folder cannot be renamed.", back)
		return
	}
	if name == path.Base(fp) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	if _, err := h.plugin.Rename(username, fp, path.Join(parentDir(fp), name)); err != nil {
		h.flashRedirect(w, r, "error", "“"+path.Base(fp)+"” could not be renamed: "+err.Error(), back)
		return
	}
	h.flashRedirect(w, r, "success", "Renamed “"+path.Base(fp)+"” to “"+name+"”.", back)
}

func (h *Pages) handleDeleteForm(w http.ResponseWriter, r *http.Request, username, fp string) {
	back := fileURL(fp)
	if err := r.ParseForm(); err != nil {
		h.flashRedirect(w, r, "error", "Nothing was deleted: "+err.Error(), back)
		return
	}
	paths := r.PostForm["path"]
	if len(paths) == 0 {
		h.flashRedirect(w, r, "error", "Nothing was selected to delete.", back)
		return
	}
	var failed []string
	for _, p := range paths {
		p = path.Clean("/" + p)
		if p == "/" {
			failed = append(failed, "the root folder")
			continue
		}
		if err := h.plugin.Delete(username, p); err != nil {
			h.plugin.log.Error("delete from file browser", "user", username, "path", p, "err", err)
			failed = append(failed, "“"+path.Base(p)+"” ("+err.Error()+")")
		}
	}
	switch {
	case len(failed) > 0:
		h.flashRedirect(w, r, "error", "Could not delete "+strings.Join(failed, ", ")+".", back)
	case len(paths) == 1:
		h.flashRedirect(w, r, "success", "Moved “"+path.Base(paths[0])+"” to the trash.", back)
	default:
		h.flashRedirect(w, r, "success", fmt.Sprintf("Moved %d items to the trash.", len(paths)), back)
	}
}

// handleStar toggles a favourite for an htmx star button and returns the
// button in its new state.
func (h *Pages) handleStar(w http.ResponseWriter, r *http.Request, username, fp string) {
	meta, err := h.plugin.GetByPath(username, fp)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := h.plugin.SetFavorite(username, fp, !meta.Favorite); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := favoriteButton(fp, !meta.Favorite).Render(r.Context(), w); err != nil {
		h.plugin.log.Error("render star", "err", err)
	}
}

func (h *Pages) handleUpload(w http.ResponseWriter, r *http.Request, username, fp string) {
	back := fileURL(fp)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		h.flashRedirect(w, r, "error", "The upload could not be read: "+err.Error(), back)
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		h.flashRedirect(w, r, "error", "Choose a file to upload.", back)
		return
	}
	for _, header := range files {
		name := path.Base(header.Filename)
		file, err := header.Open()
		if err != nil {
			h.flashRedirect(w, r, "error", "“"+name+"” could not be uploaded: "+err.Error(), back)
			return
		}
		_, uploadErr := h.plugin.Upload(username, path.Join(fp, name), file)
		file.Close()
		if uploadErr != nil {
			h.flashRedirect(w, r, "error", "“"+name+"” could not be uploaded: "+uploadErr.Error(), back)
			return
		}
	}
	if len(files) == 1 {
		h.flashRedirect(w, r, "success", "Uploaded “"+path.Base(files[0].Filename)+"”.", back)
		return
	}
	h.flashRedirect(w, r, "success", fmt.Sprintf("Uploaded %d files.", len(files)), back)
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

// --- Display helpers ---

// fileURL is the file browser's URL for a path, escaped so a name with a
// space, "#", "?" or "%" in it still reaches the right file.
func fileURL(p string) string {
	return "/apps/files" + (&url.URL{Path: path.Clean("/" + p)}).EscapedPath()
}

// archiveEntryURL is the URL of a path inside an archive.
func archiveEntryURL(archivePath, inner string) string {
	if inner == "" {
		return fileURL(archivePath)
	}
	return fileURL(archivePath + "/" + inner)
}

// dirTitle names a folder for its page heading.
func dirTitle(dirPath string) string {
	if path.Clean("/"+dirPath) == "/" {
		return "Files"
	}
	return path.Base(dirPath)
}

// dirCrumbs leads from the root to the folder that holds dirPath. A page
// header appends dirPath itself as the current page.
func dirCrumbs(dirPath string) []ui.Crumb {
	if path.Clean("/"+dirPath) == "/" {
		return nil
	}
	return pathCrumbs(parentDir(dirPath))
}

// pathCrumbs leads from the root through every folder of dirPath, dirPath
// included.
func pathCrumbs(dirPath string) []ui.Crumb {
	crumbs := []ui.Crumb{{Label: "Files", URL: fileURL("/")}}
	for _, seg := range breadcrumbSegments(dirPath) {
		crumbs = append(crumbs, ui.Crumb{Label: seg.Name, URL: fileURL(seg.Path)})
	}
	return crumbs
}

// archiveCrumbs leads from the root through the archive's folder, the
// archive, and the folders inside it above innerPath.
func archiveCrumbs(archivePath, innerPath string) []ui.Crumb {
	crumbs := pathCrumbs(parentDir(archivePath))
	if innerPath == "" {
		return crumbs
	}
	crumbs = append(crumbs, ui.Crumb{Label: path.Base(archivePath), URL: fileURL(archivePath)})
	segs := archiveInnerSegments(archivePath, innerPath)
	for _, seg := range segs[:len(segs)-1] {
		crumbs = append(crumbs, ui.Crumb{Label: seg.Name, URL: archiveEntryURL(archivePath, seg.Path)})
	}
	return crumbs
}

// archiveTitle is the heading of an archive listing: the archive, or the
// folder inside it.
func archiveTitle(archivePath, innerPath string) string {
	if innerPath == "" {
		return path.Base(archivePath)
	}
	return path.Base(innerPath)
}

// shortTime is a modification time as a listing shows it: the time for
// today, the day for this year, the date for anything older.
func shortTime(unix int64, now time.Time) string {
	if unix <= 0 {
		return ""
	}
	t := time.Unix(unix, 0).In(now.Location())
	switch {
	case t.Year() == now.Year() && t.YearDay() == now.YearDay():
		return t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}

// modifiedLabel says when a file changed, as a sentence fragment:
// "Modified today at 06:00", "Modified Mar 14".
func modifiedLabel(unix int64, now time.Time) string {
	t := shortTime(unix, now)
	if strings.Contains(t, ":") {
		return "Modified today at " + t
	}
	return "Modified " + t
}

// fullTime is the exact time, for a title attribute.
func fullTime(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).Format("Mon, Jan 2, 2006 15:04")
}

// metaLine is the size and date shown under a name on a phone, where the
// columns that hold them are hidden.
func metaLine(f FileMeta, now time.Time) string {
	var parts []string
	if !f.IsDir {
		parts = append(parts, formatSize(f.Size))
	}
	if t := shortTime(f.MTime, now); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, " · ")
}

// friendlyKind names a file's type for people: "PNG image", "Text",
// "PDF document". It never shows a MIME parameter such as "; charset=utf-8".
func friendlyKind(mimeType, name string) string {
	mt := mimeType
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	mt = strings.TrimSpace(strings.ToLower(mt))
	switch mt {
	case "text/plain":
		return "Text"
	case "text/markdown", "text/x-markdown":
		return "Markdown"
	case "text/html":
		return "HTML document"
	case "text/csv":
		return "CSV spreadsheet"
	case "application/json":
		return "JSON"
	case "application/pdf":
		return "PDF document"
	case "application/zip":
		return "ZIP archive"
	case "application/x-tar":
		return "TAR archive"
	case "application/gzip", "application/x-gzip":
		return "GZIP archive"
	case "audio/mpeg":
		return "MP3 audio"
	case "image/svg+xml":
		return "SVG image"
	}
	major, sub, _ := strings.Cut(mt, "/")
	sub = strings.TrimPrefix(sub, "x-")
	switch major {
	case "image":
		return strings.ToUpper(sub) + " image"
	case "video":
		return strings.ToUpper(sub) + " video"
	case "audio":
		return strings.ToUpper(sub) + " audio"
	case "text":
		return "Text"
	}
	if ext := strings.TrimPrefix(path.Ext(name), "."); ext != "" {
		return strings.ToUpper(ext) + " file"
	}
	return "File"
}

// archiveMetaLine is metaLine for an entry inside an archive.
func archiveMetaLine(e ArchiveEntry, now time.Time) string {
	var parts []string
	if !e.IsDir {
		parts = append(parts, formatSize(e.Size))
	}
	if t := formatArchiveTime(e.MTime, now); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, " · ")
}

// extractFolderName is the folder "Extract all" creates for an archive.
func extractFolderName(archivePath string) string {
	return path.Base(ExtractDestination(archivePath))
}

// menuID is the DOM id of a row's action menu. It uses the file's ID, which
// unlike its path cannot collide once reduced to DOM-safe characters.
func menuID(id int64) string {
	return "file-menu-" + strconv.FormatInt(id, 10)
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
