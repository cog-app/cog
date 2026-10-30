package notes

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// NotesAPI implements the NextCloud Notes API v1 endpoints.
type NotesAPI struct {
	plugin *Plugin
}

// NewNotesAPI creates a new NC Notes API handler.
func NewNotesAPI(p *Plugin) *NotesAPI {
	return &NotesAPI{plugin: p}
}

func (a *NotesAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		plugin.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	db, err := a.plugin.DB(u.Username)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	username := u.Username
	if err := a.plugin.ensureNotesDirFor(username); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	path := r.URL.Path
	for _, prefix := range []string{
		"/index.php/apps/notes/api/v1.3",
		"/index.php/apps/notes/api/v1",
		"/index.php/apps/notes/api/v0.2",
		"/index.php/apps/notes/api",
	} {
		if after, ok := strings.CutPrefix(path, prefix); ok {
			path = after
			break
		}
	}
	if path == "" {
		path = "/"
	}

	switch {
	case r.Method == "GET" && path == "/notes":
		a.listNotes(w, r, db, username)
	case r.Method == "GET" && strings.HasPrefix(path, "/notes/"):
		id := plugin.PathID(path, "/notes/")
		a.getNote(w, r, db, username, id)
	case r.Method == "POST" && path == "/notes":
		a.createNote(w, r, db, username)
	case r.Method == "PUT" && strings.HasPrefix(path, "/notes/"):
		id := plugin.PathID(path, "/notes/")
		a.updateNote(w, r, db, username, id)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/notes/"):
		id := plugin.PathID(path, "/notes/")
		a.deleteNote(w, db, username, id)
	case r.Method == "GET" && path == "/settings":
		a.getSettings(w)
	case r.Method == "PUT" && path == "/settings":
		a.putSettings(w, r)
	default:
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *NotesAPI) listNotes(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	category := r.URL.Query().Get("category")
	exclude := r.URL.Query().Get("exclude")

	var notes []Note
	var err error
	if category != "" {
		notes, err = NotesByCategory(db, category, 1000)
	} else {
		notes, err = ListNotes(db, 1000)
	}
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	out := make([]map[string]any, len(notes))
	for i := range notes {
		out[i] = a.noteToAPI(&notes[i], username, exclude != "content")
	}
	plugin.WriteJSON(w, http.StatusOK, out)
}

func (a *NotesAPI) getNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// If-None-Match support
	if match := r.Header.Get("If-None-Match"); match != "" && match == note.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("ETag", note.ETag)
	plugin.WriteJSON(w, http.StatusOK, a.noteToAPI(note, username, true))
}

func (a *NotesAPI) createNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	var body struct {
		Title    string `json:"title"`
		Content  string `json:"content"`
		Category string `json:"category"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Title == "" {
		body.Title = "New Note"
	}

	filename := FilenameFromTitle(body.Title)
	etag := ComputeETag(body.Content)

	if err := a.plugin.writeNoteContent(username, filename, body.Content); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	note, err := CreateNoteMeta(db, filename, body.Category, etag)
	if err != nil {
		plugin.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	if body.Favorite {
		ToggleFavorite(db, note.ID)
		note, _ = GetNote(db, note.ID)
	}

	w.Header().Set("ETag", note.ETag)
	plugin.WriteJSON(w, http.StatusOK, a.noteToAPI(note, username, true))
}

func (a *NotesAPI) updateNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// If-Match concurrency check
	if match := r.Header.Get("If-Match"); match != "" && match != note.ETag {
		w.Header().Set("ETag", note.ETag)
		plugin.WriteJSON(w, http.StatusPreconditionFailed, a.noteToAPI(note, username, true))
		return
	}

	var body struct {
		Title    *string `json:"title"`
		Content  *string `json:"content"`
		Category *string `json:"category"`
		Favorite *bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	filename := note.Filename
	category := note.Category

	if body.Title != nil {
		newFilename := FilenameFromTitle(*body.Title)
		if newFilename != filename {
			if err := a.plugin.renameNoteFile(username, filename, newFilename); err != nil {
				plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			filename = newFilename
		}
	}

	if body.Content != nil {
		if err := a.plugin.writeNoteContent(username, filename, *body.Content); err != nil {
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	if body.Category != nil {
		category = *body.Category
	}

	// Compute etag from current file content
	content, _ := a.plugin.readNoteContent(username, filename)
	etag := ComputeETag(content)

	if err := UpdateNoteMeta(db, id, filename, category, etag); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if body.Favorite != nil {
		currentNote, _ := GetNote(db, id)
		if currentNote != nil && currentNote.Favorite != *body.Favorite {
			ToggleFavorite(db, id)
		}
	}

	note, _ = GetNote(db, id)
	w.Header().Set("ETag", note.ETag)
	plugin.WriteJSON(w, http.StatusOK, a.noteToAPI(note, username, true))
}

func (a *NotesAPI) deleteNote(w http.ResponseWriter, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err := a.plugin.deleteNote(db, username, note); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NotesAPI) getSettings(w http.ResponseWriter) {
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"notesPath":  "Notes",
		"fileSuffix": ".md",
	})
}

func (a *NotesAPI) putSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"notesPath":  "Notes",
		"fileSuffix": ".md",
	})
}

// --- Helpers ---

func (a *NotesAPI) noteToAPI(n *Note, username string, includeContent bool) map[string]any {
	m := map[string]any{
		"id":       n.ID,
		"etag":     n.ETag,
		"title":    n.Title(),
		"category": n.Category,
		"favorite": n.Favorite,
		"modified": n.Modified,
		"readonly": false,
	}
	if includeContent {
		content, _ := a.plugin.readNoteContent(username, n.Filename)
		m["content"] = content
	}
	return m
}
