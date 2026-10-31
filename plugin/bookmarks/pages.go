package bookmarks

import (
	"database/sql"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/ui"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the bookmarks plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type sidebarData struct {
	Folders      []Folder
	FolderCounts map[int64]int
	TagCount     int
	TotalCount   int
	ActiveFolder int64
	ActivePath   string
	// OOB renders the sidebar as an out-of-band swap, so deleting a row
	// brings its counts up to date along with the list.
	OOB bool
}

// The lists a bookmark can be shown in.
const (
	viewAll    = "all"
	viewFolder = "folder"
	viewTag    = "tag"
)

// listView is one list of bookmarks and what it needs to render.
type listView struct {
	Kind      string
	Search    string
	Bookmarks []Bookmark
	Now       time.Time
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string, activeFolder int64) (sidebarData, error) {
	folders, err := ListFolders(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("list folders: %w", err)
	}
	counts, err := FolderCounts(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("count folders: %w", err)
	}
	tags, err := ListAllTags(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("list tags: %w", err)
	}
	total, err := BookmarkCount(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("count bookmarks: %w", err)
	}
	return sidebarData{
		Folders:      folders,
		FolderCounts: counts,
		TagCount:     len(tags),
		TotalCount:   total,
		ActiveFolder: activeFolder,
		ActivePath:   activePath,
	}, nil
}

// pageSetup opens the user's database and builds the sidebar for a page. It
// writes the error response itself and returns ok=false when it fails.
func (h *Pages) pageSetup(w http.ResponseWriter, r *http.Request, activePath string, activeFolder int64) (*sql.DB, sidebarData, bool) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	sb, err := h.buildSidebar(db, activePath, activeFolder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	return db, sb, true
}

// Index shows all bookmarks.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/bookmarks/", 0)
	if !ok {
		return
	}
	search := r.URL.Query().Get("q")
	bms, _, err := ListBookmarks(db, nil, nil, search, "lastmodified", 0, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Bookmarks")
	ui.Render(w, r, bookmarksIndexPage(sb, listView{Kind: viewAll, Search: search, Bookmarks: bms, Now: h.plugin.ctx.Now()}))
}

// pathID parses the {id} path value. It writes the error response itself and
// returns ok=false when the value is not a number.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return 0, false
	}
	return id, true
}

// notFoundOr writes 404 for a missing row and 500 for anything else.
func notFoundOr(w http.ResponseWriter, err error, what string) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, what+" not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

// FolderView shows bookmarks in a folder.
func (h *Pages) FolderView(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	db, sb, ok := h.pageSetup(w, r, "/apps/bookmarks/folder/"+r.PathValue("id"), id)
	if !ok {
		return
	}
	folder, err := GetFolder(db, id)
	if err != nil {
		notFoundOr(w, err, "folder")
		return
	}
	bms, _, err := ListBookmarks(db, &id, nil, "", "lastmodified", 0, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, folder.Title)
	ui.Render(w, r, bookmarksFolderPage(sb, folder, listView{Kind: viewFolder, Bookmarks: bms, Now: h.plugin.ctx.Now()}))
}

// TagsView shows every tag with its number of bookmarks.
func (h *Pages) TagsView(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/bookmarks/tags", 0)
	if !ok {
		return
	}
	tags, err := ListTagCounts(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Tags")
	ui.Render(w, r, bookmarksTagsPage(sb, tags))
}

// TagView shows bookmarks for a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	tag := r.PathValue("tag")
	db, sb, ok := h.pageSetup(w, r, "/apps/bookmarks/tags", 0)
	if !ok {
		return
	}
	bms, _, err := ListBookmarks(db, nil, []string{tag}, "", "lastmodified", 0, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Tag: "+tag)
	ui.Render(w, r, bookmarksTagPage(sb, tag, listView{Kind: viewTag, Bookmarks: bms, Now: h.plugin.ctx.Now()}))
}

// ImportExport shows the import form and the export download.
func (h *Pages) ImportExport(w http.ResponseWriter, r *http.Request) {
	_, sb, ok := h.pageSetup(w, r, "/apps/bookmarks/import-export", 0)
	if !ok {
		return
	}
	plugin.SetTitle(w, "Import and export")
	ui.Render(w, r, bookmarksImportExportPage(sb, plugin.CSRFToken(r)))
}

// loadBookmark reads the {id} path value and the bookmark it names. It writes
// the error response itself and returns nil when it fails.
func loadBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) *Bookmark {
	id, ok := pathID(w, r)
	if !ok {
		return nil
	}
	bm, err := GetBookmark(db, id)
	if err != nil {
		notFoundOr(w, err, "bookmark")
		return nil
	}
	return bm
}

// BookmarkView shows a single bookmark detail.
func (h *Pages) BookmarkView(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "", 0)
	if !ok {
		return
	}
	bm := loadBookmark(w, r, db)
	if bm == nil {
		return
	}
	plugin.SetTitle(w, bm.Title)
	ui.Render(w, r, bookmarkDetailPage(sb, bm, h.plugin.ctx.Now()))
}

// BookmarkEdit shows the edit form.
func (h *Pages) BookmarkEdit(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "", 0)
	if !ok {
		return
	}
	bm := loadBookmark(w, r, db)
	if bm == nil {
		return
	}
	plugin.SetTitle(w, "Edit "+bm.Title)
	ui.Render(w, r, bookmarkEditPage(sb, bm, plugin.CSRFToken(r)))
}

// NewBookmark shows the create form. ?folder=ID ticks that folder, so "Add
// bookmark" on a folder's page adds to it.
func (h *Pages) NewBookmark(w http.ResponseWriter, r *http.Request) {
	_, sb, ok := h.pageSetup(w, r, "", 0)
	if !ok {
		return
	}
	var selected []int64
	if id, err := strconv.ParseInt(r.URL.Query().Get("folder"), 10, 64); err == nil {
		selected = []int64{id}
	}
	plugin.SetTitle(w, "New bookmark")
	ui.Render(w, r, bookmarkNewPage(sb, selected, plugin.CSRFToken(r)))
}

// CreateBookmark handles POST to create a new bookmark.
func (h *Pages) CreateBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	bmURL := strings.TrimSpace(r.FormValue("url"))
	if bmURL == "" {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "A bookmark needs a URL.", Form: map[string]string{
			"title": r.FormValue("title"), "description": r.FormValue("description"), "tags": r.FormValue("tags"),
		}})
		http.Redirect(w, r, "/apps/bookmarks/new", http.StatusSeeOther)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = bmURL
	}
	tags := parseTags(r.FormValue("tags"))
	folders, err := formFolderIDs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	bm, err := CreateBookmark(db, bmURL, title, r.FormValue("description"), tags, folders)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/bookmarks/bookmark/"+strconv.FormatInt(bm.ID, 10), http.StatusSeeOther)
}

// UpdateBookmark handles POST to update an existing bookmark.
func (h *Pages) UpdateBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	description := r.FormValue("description")
	// An empty field clears the tags; nil would leave them as they were.
	tags := parseTags(r.FormValue("tags"))
	if tags == nil {
		tags = []string{}
	}
	folders, err := formFolderIDs(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if folders == nil {
		folders = []int64{}
	}

	if _, err := UpdateBookmark(db, id, &title, &description, tags, folders); err != nil {
		notFoundOr(w, err, "bookmark")
		return
	}
	http.Redirect(w, r, "/apps/bookmarks/bookmark/"+r.PathValue("id"), http.StatusSeeOther)
}

// DeleteBookmark handles DELETE to remove a bookmark.
//
// From a list row (HX-Target names the row) the response removes the row and
// brings the sidebar, and the list's empty state, up to date out of band.
// From the bookmark's own page, which no longer exists, the browser is sent
// to the list with a message.
func (h *Pages) DeleteBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bm := loadBookmark(w, r, db)
	if bm == nil {
		return
	}
	if err := DeleteBookmark(db, bm.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Target") != bookmarkRowID(bm.ID) {
		redirectWithFlash(w, r, "/apps/bookmarks/", fmt.Sprintf("Deleted “%s”.", bm.Title))
		return
	}

	page, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		page = &url.URL{}
	}
	sb, err := h.buildSidebar(db, page.Path, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb.OOB = true
	if strings.HasPrefix(page.Path, "/apps/bookmarks/folder/") {
		if id, err := activeFolder(page.Path); err == nil {
			sb.ActiveFolder = id
		}
	}
	if strings.HasPrefix(page.Path, "/apps/bookmarks/tag/") {
		sb.ActivePath = "/apps/bookmarks/tags"
	}

	empty, err := h.emptyView(db, page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, rowDeleted(sb, empty))
}

// emptyView returns the list the page shows when that list is now empty, or
// nil when it still has bookmarks or the page is not a list.
func (h *Pages) emptyView(db *sql.DB, page *url.URL) (*listView, error) {
	var (
		v      listView
		folder *int64
		tags   []string
	)
	switch {
	case page.Path == "/apps/bookmarks/" || page.Path == "/apps/bookmarks":
		v = listView{Kind: viewAll, Search: page.Query().Get("q")}
	case strings.HasPrefix(page.Path, "/apps/bookmarks/folder/"):
		id, err := activeFolder(page.Path)
		if err != nil {
			return nil, nil
		}
		v, folder = listView{Kind: viewFolder}, &id
	case strings.HasPrefix(page.Path, "/apps/bookmarks/tag/"):
		v, tags = listView{Kind: viewTag}, []string{strings.TrimPrefix(page.Path, "/apps/bookmarks/tag/")}
	default:
		return nil, nil
	}
	_, total, err := ListBookmarks(db, folder, tags, v.Search, "", 0, 1)
	if err != nil {
		return nil, err
	}
	if total > 0 {
		return nil, nil
	}
	return &v, nil
}

func activeFolder(path string) (int64, error) {
	return strconv.ParseInt(strings.TrimPrefix(path, "/apps/bookmarks/folder/"), 10, 64)
}

// redirectWithFlash ends an action with a message on the page it leads to.
// An HTMX request is redirected with HX-Redirect, which reloads the page; a
// plain form post with 303.
func redirectWithFlash(w http.ResponseWriter, r *http.Request, to, msg string) {
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: msg})
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", to)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// OpenBookmark counts a visit and sends the browser to the bookmark's URL. It
// is a GET so the bookmark's title can be an ordinary link: it opens in a new
// tab, can be middle-clicked, and its target shows on hover.
func (h *Pages) OpenBookmark(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	bm := loadBookmark(w, r, db)
	if bm == nil {
		return
	}
	if err := RecordClick(db, bm.URL); err != nil {
		h.plugin.log.Error("record bookmark click", "bookmark", bm.ID, "error", err)
	}
	http.Redirect(w, r, bm.URL, http.StatusSeeOther)
}

// RecordClick handles POST to increment click count.
func (h *Pages) RecordClick(w http.ResponseWriter, r *http.Request) {
	h.OpenBookmark(w, r)
}

// CreateFolder handles POST to create a folder.
func (h *Pages) CreateFolder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "A folder needs a name."})
		http.Redirect(w, r, "/apps/bookmarks/", http.StatusSeeOther)
		return
	}
	var parentID *int64
	if pid := r.FormValue("parent_id"); pid != "" {
		v, err := strconv.ParseInt(pid, 10, 64)
		if err != nil {
			http.Error(w, "invalid parent_id", http.StatusBadRequest)
			return
		}
		parentID = &v
	}
	f, err := CreateFolder(db, title, parentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Created folder “%s”.", f.Title)})
	http.Redirect(w, r, fmt.Sprintf("/apps/bookmarks/folder/%d", f.ID), http.StatusSeeOther)
}

// UpdateFolder handles POST to rename a folder.
func (h *Pages) UpdateFolder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	folder, err := GetFolder(db, id)
	if err != nil {
		notFoundOr(w, err, "folder")
		return
	}
	to := "/apps/bookmarks/folder/" + r.PathValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "A folder needs a name."})
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	if err := UpdateFolder(db, id, title, folder.ParentID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: "Folder renamed."})
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// DeleteFolder handles DELETE to remove a folder. Its page no longer exists,
// so the browser goes to the list.
func (h *Pages) DeleteFolder(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	folder, err := GetFolder(db, id)
	if err != nil {
		notFoundOr(w, err, "folder")
		return
	}
	if err := DeleteFolder(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	redirectWithFlash(w, r, "/apps/bookmarks/", fmt.Sprintf("Deleted folder “%s”. Its bookmarks were kept.", folder.Title))
}

// RenameTag handles POST to rename a tag on every bookmark.
func (h *Pages) RenameTag(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	oldTag := r.FormValue("tag")
	newTag := strings.TrimSpace(r.FormValue("name"))
	if strings.Contains(newTag, ",") {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "A tag cannot contain a comma."})
		http.Redirect(w, r, "/apps/bookmarks/tag/"+url.PathEscape(oldTag), http.StatusSeeOther)
		return
	}
	if oldTag == "" || newTag == "" {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "A tag needs a name."})
		http.Redirect(w, r, "/apps/bookmarks/tag/"+url.PathEscape(oldTag), http.StatusSeeOther)
		return
	}
	if err := RenameTag(db, oldTag, newTag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Renamed “%s” to “%s”.", oldTag, newTag)})
	http.Redirect(w, r, "/apps/bookmarks/tag/"+url.PathEscape(newTag), http.StatusSeeOther)
}

// DeleteTag handles POST to remove a tag from every bookmark. The bookmarks
// themselves stay.
func (h *Pages) DeleteTag(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tag := r.FormValue("tag")
	if err := DeleteTag(db, tag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Removed the tag “%s”. Its bookmarks were kept.", tag)})
	http.Redirect(w, r, "/apps/bookmarks/tags", http.StatusSeeOther)
}

func parseTags(s string) []string {
	if s == "" {
		return nil
	}
	var tags []string
	for t := range strings.SplitSeq(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// ImportBookmarks handles POST to import bookmarks from Netscape HTML format.
func (h *Pages) ImportBookmarks(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "Choose a bookmarks file to import."})
		http.Redirect(w, r, "/apps/bookmarks/import-export", http.StatusSeeOther)
		return
	}
	defer file.Close()

	count, err := ImportNetscapeHTML(db, file)
	if err != nil {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: fmt.Sprintf("Import stopped after %d bookmarks: %v", count, err)})
		http.Redirect(w, r, "/apps/bookmarks/import-export", http.StatusSeeOther)
		return
	}
	plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Imported %d bookmarks.", count)})
	http.Redirect(w, r, "/apps/bookmarks/", http.StatusSeeOther)
}

// ExportBookmarks handles GET to export bookmarks as Netscape HTML format.
func (h *Pages) ExportBookmarks(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var buf strings.Builder
	if err := ExportNetscapeHTML(db, &buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=bookmarks.html")
	if _, err := w.Write([]byte(buf.String())); err != nil {
		h.plugin.log.Error("write bookmarks export", "error", err)
	}
}

// formFolderIDs reads the ticked folder_id values. The form sends one value
// per ticked folder; the API's comma-separated form is accepted too.
func formFolderIDs(r *http.Request) ([]int64, error) {
	if err := r.ParseForm(); err != nil {
		return nil, err
	}
	var ids []int64
	for _, v := range r.Form["folder_id"] {
		for part := range strings.SplitSeq(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid folder_id %q", part)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func bookmarkRowID(id int64) string {
	return fmt.Sprintf("bm-%d", id)
}
