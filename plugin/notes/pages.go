package notes

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

// Pages holds the web UI handlers for the notes plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type viewExtras struct {
	NeedsKaTeX   bool
	NeedsMermaid bool
}

type sidebarData struct {
	NoteCount    int
	StarredCount int
	Categories   []string
	ActivePath   string
	// OOB renders the sidebar as an out-of-band swap, so a row action can
	// bring its counts up to date along with the row.
	OOB bool
}

// The lists a note can be shown in. Each has its own empty state.
const (
	viewAll      = "all"
	viewStarred  = "starred"
	viewCategory = "category"
)

// listView is one list of notes and what it needs to render.
type listView struct {
	Kind     string
	Category string
	Notes    []Note
	Now      time.Time
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) (sidebarData, error) {
	noteCount, err := NoteCount(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("count notes: %w", err)
	}
	starredCount, err := StarredCount(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("count starred notes: %w", err)
	}
	categories, err := ListCategories(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("list categories: %w", err)
	}
	return sidebarData{
		NoteCount:    noteCount,
		StarredCount: starredCount,
		Categories:   categories,
		ActivePath:   activePath,
	}, nil
}

// pageSetup opens the user's database and builds the sidebar for a page. It
// writes the error response itself and returns ok=false when it fails.
func (h *Pages) pageSetup(w http.ResponseWriter, r *http.Request, activePath string) (*sql.DB, sidebarData, bool) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	sb, err := h.buildSidebar(db, activePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	return db, sb, true
}

// Index shows all notes.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	if err := h.plugin.ensureNotesDirFor(plugin.Username(r)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	db, sb, ok := h.pageSetup(w, r, "/apps/notes/")
	if !ok {
		return
	}
	notes, err := ListNotes(db, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Notes")
	ui.Render(w, r, notesListPage(sb, "All notes", listView{Kind: viewAll, Notes: notes, Now: h.plugin.ctx.Now()}))
}

// CategoryView shows notes filtered by category.
func (h *Pages) CategoryView(w http.ResponseWriter, r *http.Request) {
	category := r.PathValue("path")
	db, sb, ok := h.pageSetup(w, r, "/apps/notes/category/"+category)
	if !ok {
		return
	}
	notes, err := NotesByCategory(db, category, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, category)
	ui.Render(w, r, notesListPage(sb, category, listView{Kind: viewCategory, Category: category, Notes: notes, Now: h.plugin.ctx.Now()}))
}

// Starred shows favorite notes.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/notes/starred")
	if !ok {
		return
	}
	notes, err := StarredNotes(db, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Starred notes")
	ui.Render(w, r, notesListPage(sb, "Starred notes", listView{Kind: viewStarred, Notes: notes, Now: h.plugin.ctx.Now()}))
}

// loadNote reads the {id} path value and the note it names. It writes the
// error response itself and returns nil when it fails.
func loadNote(w http.ResponseWriter, r *http.Request, db *sql.DB) *Note {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid note id", http.StatusBadRequest)
		return nil
	}
	note, err := GetNote(db, id)
	if err == sql.ErrNoRows {
		http.Error(w, "note not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	return note
}

// NoteView shows a single note rendered as HTML.
func (h *Pages) NoteView(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/notes/note/"+r.PathValue("id"))
	if !ok {
		return
	}
	note := loadNote(w, r, db)
	if note == nil {
		return
	}
	content, err := h.plugin.readNoteContent(plugin.Username(r), note.Filename)
	if err != nil {
		http.Error(w, fmt.Sprintf("read %s: %v", note.Filename, err), http.StatusInternalServerError)
		return
	}
	extras := viewExtras{
		NeedsKaTeX:   NeedsKaTeX(content),
		NeedsMermaid: NeedsMermaid(content),
	}
	plugin.SetTitle(w, note.Title())
	ui.Render(w, r, noteViewPage(sb, note, RenderMarkdown(content), h.plugin.ctx.Now(), extras))
}

// NoteEdit shows the edit form for a note.
func (h *Pages) NoteEdit(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/notes/note/"+r.PathValue("id")+"/edit")
	if !ok {
		return
	}
	note := loadNote(w, r, db)
	if note == nil {
		return
	}
	content, err := h.plugin.readNoteContent(plugin.Username(r), note.Filename)
	if err != nil {
		http.Error(w, fmt.Sprintf("read %s: %v", note.Filename, err), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Edit "+note.Title())
	ui.Render(w, r, noteEditPage(sb, note, content, sb.Categories, plugin.CSRFToken(r)))
}

// NewNote shows the create form.
func (h *Pages) NewNote(w http.ResponseWriter, r *http.Request) {
	_, sb, ok := h.pageSetup(w, r, "/apps/notes/new")
	if !ok {
		return
	}
	plugin.SetTitle(w, "New note")
	ui.Render(w, r, noteNewPage(sb, sb.Categories, plugin.CSRFToken(r)))
}

// Preview renders the editor's Markdown the way the note page will show it.
// It is an HTMX fragment: the editor's Preview tab swaps it in.
func (h *Pages) Preview(w http.ResponseWriter, r *http.Request) {
	ui.Render(w, r, notePreview(RenderMarkdown(r.FormValue("content"))))
}

// CreateNote handles POST to create a new note.
func (h *Pages) CreateNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	username := plugin.Username(r)
	if err := h.plugin.ensureNotesDirFor(username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	title := r.FormValue("title")
	if title == "" {
		title = "Untitled"
	}
	content := r.FormValue("content")
	category := r.FormValue("category")

	filename, err := h.plugin.freeNoteFilename(db, username, title, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	etag := ComputeETag(content)

	if err := h.plugin.writeNoteContent(username, filename, content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	note, err := CreateNoteMeta(db, filename, category, etag)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/notes/note/"+strconv.FormatInt(note.ID, 10), http.StatusSeeOther)
}

// UpdateNote handles POST to update an existing note.
func (h *Pages) UpdateNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	note := loadNote(w, r, db)
	if note == nil {
		return
	}
	id := note.ID
	username := plugin.Username(r)

	// Someone else saved since this form was loaded: send the user back to
	// the current version rather than overwrite it.
	if formEtag := r.FormValue("etag"); formEtag != "" && formEtag != note.ETag {
		plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: "This note changed since you opened it. Your edit was not saved; here is the current version."})
		http.Redirect(w, r, "/apps/notes/note/"+r.PathValue("id")+"/edit", http.StatusSeeOther)
		return
	}

	title := r.FormValue("title")
	content := r.FormValue("content")
	category := r.FormValue("category")

	filename := note.Filename
	if title != "" {
		newFilename, err := h.plugin.freeNoteFilename(db, username, title, note)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if newFilename != filename {
			if err := h.plugin.renameNoteFile(username, filename, newFilename); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			filename = newFilename
		}
	}

	if err := h.plugin.writeNoteContent(username, filename, content); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	etag := ComputeETag(content)
	if err := UpdateNoteMeta(db, id, filename, category, etag); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/apps/notes/note/"+r.PathValue("id"), http.StatusSeeOther)
}

// DeleteNote handles DELETE to remove a note.
//
// From a list row (HX-Target names the row) the response removes the row and
// brings the sidebar, and the list's empty state, up to date out of band.
// From anywhere else, the note's own page included, the page the user was on
// no longer exists, so the browser is sent to the list with a message.
func (h *Pages) DeleteNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	note := loadNote(w, r, db)
	if note == nil {
		return
	}
	if err := h.plugin.deleteNote(db, plugin.Username(r), note); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if r.Header.Get("HX-Target") != noteRowID(note.ID) {
		plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Deleted “%s”.", note.Title())})
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/apps/notes/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/apps/notes/", http.StatusSeeOther)
		return
	}

	h.renderRowUpdate(w, r, db, nil)
}

// ToggleStar handles POST to toggle favorite state of a note.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	note := loadNote(w, r, db)
	if note == nil {
		return
	}
	if err := ToggleFavorite(db, note.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	note.Favorite = !note.Favorite

	// Keep the file's favourite in the files app in step. The note's own
	// state is already saved, so a failure here is logged, not returned.
	if h.plugin.files != nil {
		if err := h.plugin.files.SetFavorite(plugin.Username(r), notesPath(note.Filename), note.Favorite); err != nil {
			h.plugin.log.Error("sync note favourite to files", "note", note.ID, "error", err)
		}
	}

	h.renderRowUpdate(w, r, db, ui.Star(note.Favorite, fmt.Sprintf("/apps/notes/notes/%d/star", note.ID)))
}

// renderRowUpdate writes the response to a row action: the swapped element
// (nil when the row is gone), the sidebar with its new counts, and — when the
// list the user is looking at is now empty — that list's empty state. The
// page is identified from HX-Current-URL, which HTMX sends with every request.
func (h *Pages) renderRowUpdate(w http.ResponseWriter, r *http.Request, db *sql.DB, swapped templ.Component) {
	page := currentPath(r)
	sb, err := h.buildSidebar(db, page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb.OOB = true

	var empty *listView
	if kind, category, ok := listKind(page); ok {
		var n int
		switch kind {
		case viewAll:
			n = sb.NoteCount
		case viewStarred:
			n = sb.StarredCount
		case viewCategory:
			if n, err = CategoryCount(db, category); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if n == 0 {
			empty = &listView{Kind: kind, Category: category}
		}
	}
	ui.Render(w, r, rowUpdate(swapped, sb, empty))
}

// currentPath is the path of the page an HTMX request was made from.
func currentPath(r *http.Request) string {
	u, err := url.Parse(r.Header.Get("HX-Current-URL"))
	if err != nil {
		return ""
	}
	return u.Path
}

// listKind reports which list a page path shows, if it is a list at all.
func listKind(path string) (kind, category string, ok bool) {
	switch {
	case path == "/apps/notes/" || path == "/apps/notes":
		return viewAll, "", true
	case path == "/apps/notes/starred":
		return viewStarred, "", true
	case strings.HasPrefix(path, "/apps/notes/category/"):
		return viewCategory, strings.TrimPrefix(path, "/apps/notes/category/"), true
	}
	return "", "", false
}

func noteRowID(id int64) string {
	return fmt.Sprintf("note-%d", id)
}
