package notes

import (
	"database/sql"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/plugin"
)

// Pages holds the web UI handlers for the notes plugin.
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

// setTitle sets the page title for the shell middleware.
func setTitle(w http.ResponseWriter, title string) {
	w.Header().Set("X-Page-Title", title)
}

// Index shows all notes.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nd := h.notesDir(r)
	EnsureNotesDir(nd)

	sb := h.buildSidebar(db, "/apps/notes/")
	notes, _ := ListNotes(db, 200)
	setTitle(w, "Notes")
	notesIndexPage(sb, notes, h.csrfToken(r)).Render(r.Context(), w)
}

// CategoryView shows notes filtered by category.
func (h *Pages) CategoryView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	category := r.PathValue("path")
	sb := h.buildSidebar(db, "/apps/notes/category/"+category)
	notes, _ := NotesByCategory(db, category, 200)
	setTitle(w, category)
	notesCategoryPage(sb, category, notes, h.csrfToken(r)).Render(r.Context(), w)
}

// Starred shows favorite notes.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/notes/starred")
	notes, _ := StarredNotes(db, 200)
	setTitle(w, "Starred Notes")
	notesStarredPage(sb, notes, h.csrfToken(r)).Render(r.Context(), w)
}

// NoteView shows a single note rendered as HTML.
func (h *Pages) NoteView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	nd := h.notesDir(r)
	content, _ := ReadContent(nd, note.Filename)
	rendered := RenderMarkdown(content)

	sb := h.buildSidebar(db, "/apps/notes/note/"+r.PathValue("id"))
	extras := viewExtras{
		NeedsKaTeX:   NeedsKaTeX(content),
		NeedsMermaid: NeedsMermaid(content),
	}
	setTitle(w, note.Title())
	noteViewPage(sb, note, rendered, h.csrfToken(r), extras).Render(r.Context(), w)
}

// NoteEdit shows the edit form for a note.
func (h *Pages) NoteEdit(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
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
	nd := h.notesDir(r)
	content, _ := ReadContent(nd, note.Filename)

	categories, _ := ListCategories(db)
	sb := h.buildSidebar(db, "/apps/notes/note/"+r.PathValue("id")+"/edit")
	setTitle(w, "Edit: "+note.Title())
	noteEditPage(sb, note, content, categories, h.csrfToken(r)).Render(r.Context(), w)
}

// NewNote shows the create form.
func (h *Pages) NewNote(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	categories, _ := ListCategories(db)
	sb := h.buildSidebar(db, "/apps/notes/new")
	setTitle(w, "New Note")
	noteNewPage(sb, categories, h.csrfToken(r)).Render(r.Context(), w)
}

// CreateNote handles POST to create a new note.
func (h *Pages) CreateNote(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nd := h.notesDir(r)
	EnsureNotesDir(nd)

	title := r.FormValue("title")
	if title == "" {
		title = "Untitled"
	}
	content := r.FormValue("content")
	category := r.FormValue("category")

	filename := FilenameFromTitle(title)
	etag := ComputeETag(content)

	if err := WriteContent(nd, filename, content); err != nil {
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
	db, err := h.userDB(r)
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

	nd := h.notesDir(r)

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
			if err := RenameFile(nd, filename, newFilename); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			filename = newFilename
		}
	}

	if err := WriteContent(nd, filename, content); err != nil {
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
	note, err := GetNote(db, id)
	if err != nil {
		http.Error(w, "note not found", http.StatusNotFound)
		return
	}

	nd := h.notesDir(r)
	DeleteFile(nd, note.Filename)
	DeleteNoteMeta(db, id)

	w.WriteHeader(http.StatusOK)
}

// ToggleStar handles POST to toggle favorite state of a note.
func (h *Pages) ToggleStar(w http.ResponseWriter, r *http.Request) {
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
	if err := ToggleFavorite(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	note, err := GetNote(db, id)
	if err != nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = noteRow(note, h.csrfToken(r)).Render(r.Context(), w)
}
