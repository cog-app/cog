package notes

import (
	"database/sql"
	"fmt"
	"net/http"
	"strconv"

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

func (h *Pages) notesDir(r *http.Request) string {
	u := plugin.UserFromContext(r.Context())
	return h.plugin.NotesDir(u.Username)
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
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) sidebarData {
	noteCount, _ := NoteCount(db)
	starredCount, _ := StarredCount(db)
	categories, _ := ListCategories(db)
	return sidebarData{
		NoteCount:    noteCount,
		StarredCount: starredCount,
		Categories:   categories,
		ActivePath:   activePath,
	}
}

// Index shows all notes.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.plugin.ensureNotesDirFor(plugin.Username(r))

	sb := h.buildSidebar(db, "/apps/notes/")
	notes, _ := ListNotes(db, 200)
	plugin.SetTitle(w, "Notes")
	notesIndexPage(sb, notes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CategoryView shows notes filtered by category.
func (h *Pages) CategoryView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	category := r.PathValue("path")
	sb := h.buildSidebar(db, "/apps/notes/category/"+category)
	notes, _ := NotesByCategory(db, category, 200)
	plugin.SetTitle(w, category)
	notesCategoryPage(sb, category, notes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Starred shows favorite notes.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/notes/starred")
	notes, _ := StarredNotes(db, 200)
	plugin.SetTitle(w, "Starred Notes")
	notesStarredPage(sb, notes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// NoteView shows a single note rendered as HTML.
func (h *Pages) NoteView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid note id", http.StatusBadRequest)
		return
	}
	note, err := GetNote(db, id)
	if err != nil {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}
	content, _ := h.plugin.readNoteContent(plugin.Username(r), note.Filename)
	rendered := RenderMarkdown(content)

	sb := h.buildSidebar(db, "/apps/notes/note/"+r.PathValue("id"))
	extras := viewExtras{
		NeedsKaTeX:   NeedsKaTeX(content),
		NeedsMermaid: NeedsMermaid(content),
	}
	plugin.SetTitle(w, note.Title())
	noteViewPage(sb, note, rendered, plugin.CSRFToken(r), extras).Render(r.Context(), w)
}

// NoteEdit shows the edit form for a note.
func (h *Pages) NoteEdit(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid note id", http.StatusBadRequest)
		return
	}
	note, err := GetNote(db, id)
	if err != nil {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}
	content, _ := h.plugin.readNoteContent(plugin.Username(r), note.Filename)

	categories, _ := ListCategories(db)
	sb := h.buildSidebar(db, "/apps/notes/note/"+r.PathValue("id")+"/edit")
	plugin.SetTitle(w, "Edit: "+note.Title())
	noteEditPage(sb, note, content, categories, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// NewNote shows the create form.
func (h *Pages) NewNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := ListCategories(db)
	sb := h.buildSidebar(db, "/apps/notes/new")
	plugin.SetTitle(w, "New Note")
	noteNewPage(sb, categories, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CreateNote handles POST to create a new note.
func (h *Pages) CreateNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	username := plugin.Username(r)
	h.plugin.ensureNotesDirFor(username)

	title := r.FormValue("title")
	if title == "" {
		title = "Untitled"
	}
	content := r.FormValue("content")
	category := r.FormValue("category")

	filename := FilenameFromTitle(title)
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
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid note id", http.StatusBadRequest)
		return
	}
	note, err := GetNote(db, id)
	if err != nil {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}

	username := plugin.Username(r)

	// Etag concurrency check
	if formEtag := r.FormValue("etag"); formEtag != "" && formEtag != note.ETag {
		http.Redirect(w, r, "/apps/notes/note/"+r.PathValue("id")+"/edit", http.StatusSeeOther)
		return
	}

	title := r.FormValue("title")
	content := r.FormValue("content")
	category := r.FormValue("category")

	filename := note.Filename
	if title != "" {
		newFilename := FilenameFromTitle(title)
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
func (h *Pages) DeleteNote(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	note, err := GetNote(db, id)
	if err != nil {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}

	h.plugin.deleteNoteFile(plugin.Username(r), note.Filename)
	DeleteNoteMeta(db, id)

	w.WriteHeader(http.StatusOK)
}

// ToggleStar handles POST to toggle favorite state of a note.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if err := ToggleFavorite(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	note, err := GetNote(db, id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Sync favorite to files plugin.
	if h.plugin.files != nil {
		username := plugin.Username(r)
		h.plugin.files.SetFavorite(username, "/Notes/"+note.Filename, note.Favorite)
	}

	ui.Star(note.Favorite, fmt.Sprintf("/apps/notes/notes/%d/star", note.ID)).Render(r.Context(), w)
}
