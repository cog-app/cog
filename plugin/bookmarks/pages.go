package bookmarks

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/plugin"
)

// Pages holds the web UI handlers for the bookmarks plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

func (h *Pages) csrfToken(r *http.Request) string {
	return plugin.CSRFTokenFromContext(r.Context())
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := plugin.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

type sidebarData struct {
	Folders      []Folder
	Tags         []string
	TotalCount   int
	ActiveFolder int64
	ActiveTag    string
	ActivePath   string
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string, activeFolder int64, activeTag string) sidebarData {
	folders, _ := ListFolders(db)
	tags, _ := ListAllTags(db)
	return sidebarData{
		Folders:      folders,
		Tags:         tags,
		TotalCount:   BookmarkCount(db),
		ActiveFolder: activeFolder,
		ActiveTag:    activeTag,
		ActivePath:   activePath,
	}
}

func setTitle(w http.ResponseWriter, title string) {
	w.Header().Set("X-Page-Title", title)
}

// Index shows all bookmarks.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	search := r.URL.Query().Get("q")
	bms, _, _ := ListBookmarks(db, nil, nil, search, "lastmodified", 0, 100)
	sb := h.buildSidebar(db, "/apps/bookmarks/", 0, "")
	setTitle(w, "Bookmarks")
	bookmarksIndexPage(sb, bms, search, h.csrfToken(r)).Render(r.Context(), w)
}

// FolderView shows bookmarks in a folder.
func (h *Pages) FolderView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	folder, err := GetFolder(db, id)
	if err != nil {
		http.Error(w, "folder not found", http.StatusNotFound)
		return
	}
	bms, _, _ := ListBookmarks(db, &id, nil, "", "lastmodified", 0, 200)
	sb := h.buildSidebar(db, "/apps/bookmarks/folder/"+r.PathValue("id"), id, "")
	setTitle(w, folder.Title)
	bookmarksFolderPage(sb, folder, bms, h.csrfToken(r)).Render(r.Context(), w)
}

// TagsView shows the tag cloud.
func (h *Pages) TagsView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/bookmarks/tags", 0, "")
	setTitle(w, "Tags")
	bookmarksTagsPage(sb, sb.Tags, h.csrfToken(r)).Render(r.Context(), w)
}

// TagView shows bookmarks for a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tag := r.PathValue("tag")
	bms, _, _ := ListBookmarks(db, nil, []string{tag}, "", "lastmodified", 0, 200)
	sb := h.buildSidebar(db, "/apps/bookmarks/tag/"+tag, 0, tag)
	setTitle(w, "Tag: "+tag)
	bookmarksTagPage(sb, tag, bms, h.csrfToken(r)).Render(r.Context(), w)
}

// BookmarkView shows a single bookmark detail.
func (h *Pages) BookmarkView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bookmark id", http.StatusBadRequest)
		return
	}
	bm, err := GetBookmark(db, id)
	if err != nil {
		http.Error(w, "bookmark not found", http.StatusNotFound)
		return
	}
	folders, _ := ListFolders(db)
	sb := h.buildSidebar(db, "", 0, "")
	setTitle(w, bm.Title)
	bookmarkDetailPage(sb, bm, folders, h.csrfToken(r)).Render(r.Context(), w)
}

// BookmarkEdit shows the edit form.
func (h *Pages) BookmarkEdit(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bookmark id", http.StatusBadRequest)
		return
	}
	bm, err := GetBookmark(db, id)
	if err != nil {
		http.Error(w, "bookmark not found", http.StatusNotFound)
		return
	}
	folders, _ := ListFolders(db)
	sb := h.buildSidebar(db, "", 0, "")
	setTitle(w, "Edit: "+bm.Title)
	bookmarkEditPage(sb, bm, folders, h.csrfToken(r)).Render(r.Context(), w)
}

// NewBookmark shows the create form.
func (h *Pages) NewBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	folders, _ := ListFolders(db)
	sb := h.buildSidebar(db, "/apps/bookmarks/new", 0, "")
	setTitle(w, "New Bookmark")
	bookmarkNewPage(sb, folders, h.csrfToken(r)).Render(r.Context(), w)
}

// CreateBookmark handles POST to create a new bookmark.
func (h *Pages) CreateBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	url := strings.TrimSpace(r.FormValue("url"))
	if url == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}
	title := r.FormValue("title")
	if title == "" {
		title = url
	}
	description := r.FormValue("description")
	tags := parseTags(r.FormValue("tags"))
	folders := parseFolderIDs(r.FormValue("folder_id"))

	bm, err := CreateBookmark(db, url, title, description, tags, folders)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/bookmarks/bookmark/"+strconv.FormatInt(bm.ID, 10), http.StatusSeeOther)
}

// UpdateBookmark handles POST to update an existing bookmark.
func (h *Pages) UpdateBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid bookmark id", http.StatusBadRequest)
		return
	}

	title := r.FormValue("title")
	description := r.FormValue("description")
	tags := parseTags(r.FormValue("tags"))
	folders := parseFolderIDs(r.FormValue("folder_id"))

	if _, err := UpdateBookmark(db, id, &title, &description, tags, folders); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/bookmarks/bookmark/"+r.PathValue("id"), http.StatusSeeOther)
}

// DeleteBookmark handles DELETE to remove a bookmark.
func (h *Pages) DeleteBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteBookmark(db, id)
	w.WriteHeader(http.StatusOK)
}

// RecordClick handles POST to increment click count.
func (h *Pages) RecordClick(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	bm, err := GetBookmark(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	RecordClick(db, bm.URL)
	http.Redirect(w, r, bm.URL, http.StatusSeeOther)
}

// CreateFolder handles POST to create a folder.
func (h *Pages) CreateFolder(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	title := r.FormValue("title")
	if title == "" {
		http.Error(w, "title is required", http.StatusBadRequest)
		return
	}
	var parentID *int64
	if pid := r.FormValue("parent_id"); pid != "" {
		if v, err := strconv.ParseInt(pid, 10, 64); err == nil {
			parentID = &v
		}
	}
	CreateFolder(db, title, parentID)
	http.Redirect(w, r, "/apps/bookmarks/", http.StatusSeeOther)
}

// UpdateFolder handles POST to update a folder.
func (h *Pages) UpdateFolder(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid folder id", http.StatusBadRequest)
		return
	}
	title := r.FormValue("title")
	UpdateFolder(db, id, title, nil)
	http.Redirect(w, r, "/apps/bookmarks/folder/"+r.PathValue("id"), http.StatusSeeOther)
}

// DeleteFolder handles DELETE to remove a folder.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteFolder(db, id)
	w.WriteHeader(http.StatusOK)
}

func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// ImportBookmarks handles POST to import bookmarks from Netscape HTML format.
func (h *Pages) ImportBookmarks(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	count, err := ImportNetscapeHTML(db, file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = count
	http.Redirect(w, r, "/apps/bookmarks/", http.StatusSeeOther)
}

// ExportBookmarks handles GET to export bookmarks as Netscape HTML format.
func (h *Pages) ExportBookmarks(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=bookmarks.html")
	ExportNetscapeHTML(db, w)
}

func parseFolderIDs(s string) []int64 {
	if s == "" {
		return nil
	}
	var ids []int64
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if v, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, v)
		}
	}
	return ids
}
