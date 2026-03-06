package notes

import (
	"database/sql"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/ui"
)

// Pages holds the web UI handlers for the notes plugin.
type Pages struct {
	plugin *Plugin
	secret []byte
	nav    dbProvider
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin, secret []byte, nav dbProvider) *Pages {
	return &Pages{plugin: p, secret: secret, nav: nav}
}

func (h *Pages) pageData(r *http.Request, title string) ui.PageData {
	u := auth.UserFromContext(r.Context())
	sessID := auth.SessionIDFromContext(r.Context())
	var csrf string
	if sessID != "" {
		csrf = auth.CSRFToken(sessID, h.secret)
	}
	var isAdmin bool
	if u != nil {
		isAdmin = u.IsAdmin
	}

	var nav []ui.NavItem
	for _, item := range h.nav.NavItems() {
		active := r.URL.Path == item.URL || (item.URL != "/" && len(r.URL.Path) >= len(item.URL) && r.URL.Path[:len(item.URL)] == item.URL)
		nav = append(nav, ui.NavItem{
			Label:  item.Label,
			URL:    item.URL,
			Active: active,
			Icon:   item.Icon,
		})
	}

	return ui.PageData{
		Title:       title,
		User:        u,
		CSRFToken:   csrf,
		Nav:         nav,
		RequestPath: r.URL.Path,
		IsAdmin:     isAdmin,
	}
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := auth.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
}

func (h *Pages) notesDir(r *http.Request) string {
	u := auth.UserFromContext(r.Context())
	return h.plugin.NotesDir(u.Username)
}

type viewExtras struct {
	NeedsKaTeX  bool
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
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	nd := h.notesDir(r)
	EnsureNotesDir(nd)

	sb := h.buildSidebar(db, "/apps/notes/")
	pd := h.pageData(r, "Notes")
	notes, _ := ListNotes(db, 200)
	notesIndexPage(pd, sb, notes, pd.CSRFToken).Render(r.Context(), w)
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
	pd := h.pageData(r, category)
	notes, _ := NotesByCategory(db, category, 200)
	notesCategoryPage(pd, sb, category, notes, pd.CSRFToken).Render(r.Context(), w)
}

// Starred shows favorite notes.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/notes/starred")
	pd := h.pageData(r, "Starred Notes")
	notes, _ := StarredNotes(db, 200)
	notesStarredPage(pd, sb, notes, pd.CSRFToken).Render(r.Context(), w)
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
	pd := h.pageData(r, note.Title())
	extras := viewExtras{
		NeedsKaTeX:  NeedsKaTeX(content),
		NeedsMermaid: NeedsMermaid(content),
	}
	noteViewPage(pd, sb, note, rendered, pd.CSRFToken, extras).Render(r.Context(), w)
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
	pd := h.pageData(r, "Edit: "+note.Title())
	noteEditPage(pd, sb, note, content, categories, pd.CSRFToken).Render(r.Context(), w)
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
	pd := h.pageData(r, "New Note")
	noteNewPage(pd, sb, categories, pd.CSRFToken).Render(r.Context(), w)
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
	_ = noteRow(note, h.pageData(r, "").CSRFToken).Render(r.Context(), w)
}
