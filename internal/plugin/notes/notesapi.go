package notes

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
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
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	db, err := a.plugin.DB(u.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	notesDir := a.plugin.NotesDir(u.Username)
	if err := EnsureNotesDir(notesDir); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		a.listNotes(w, r, db, notesDir)
	case r.Method == "GET" && strings.HasPrefix(path, "/notes/"):
		id := pathID(path, "/notes/")
		a.getNote(w, r, db, notesDir, id)
	case r.Method == "POST" && path == "/notes":
		a.createNote(w, r, db, notesDir)
	case r.Method == "PUT" && strings.HasPrefix(path, "/notes/"):
		id := pathID(path, "/notes/")
		a.updateNote(w, r, db, notesDir, id)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/notes/"):
		id := pathID(path, "/notes/")
		a.deleteNote(w, db, notesDir, id)
	case r.Method == "GET" && path == "/settings":
		a.getSettings(w)
	case r.Method == "PUT" && path == "/settings":
		a.putSettings(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *NotesAPI) listNotes(w http.ResponseWriter, r *http.Request, db *sql.DB, notesDir string) {
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	out := make([]map[string]any, len(notes))
	for i := range notes {
		out[i] = noteToAPI(&notes[i], notesDir, exclude != "content")
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *NotesAPI) getNote(w http.ResponseWriter, r *http.Request, db *sql.DB, notesDir string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// If-None-Match support
	if match := r.Header.Get("If-None-Match"); match != "" && match == note.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.Header().Set("ETag", note.ETag)
	writeJSON(w, http.StatusOK, noteToAPI(note, notesDir, true))
}

func (a *NotesAPI) createNote(w http.ResponseWriter, r *http.Request, db *sql.DB, notesDir string) {
	var body struct {
		Title    string `json:"title"`
		Content  string `json:"content"`
		Category string `json:"category"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.Title == "" {
		body.Title = "New Note"
	}

	filename := FilenameFromTitle(body.Title)
	etag := ComputeETag(body.Content)

	if err := WriteContent(notesDir, filename, body.Content); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	note, err := CreateNoteMeta(db, filename, body.Category, etag)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	if body.Favorite {
		ToggleFavorite(db, note.ID)
		note, _ = GetNote(db, note.ID)
	}

	w.Header().Set("ETag", note.ETag)
	writeJSON(w, http.StatusOK, noteToAPI(note, notesDir, true))
}

func (a *NotesAPI) updateNote(w http.ResponseWriter, r *http.Request, db *sql.DB, notesDir string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	// If-Match concurrency check
	if match := r.Header.Get("If-Match"); match != "" && match != note.ETag {
		w.Header().Set("ETag", note.ETag)
		writeJSON(w, http.StatusPreconditionFailed, noteToAPI(note, notesDir, true))
		return
	}

	var body struct {
		Title    *string `json:"title"`
		Content  *string `json:"content"`
		Category *string `json:"category"`
		Favorite *bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	filename := note.Filename
	category := note.Category

	if body.Title != nil {
		newFilename := FilenameFromTitle(*body.Title)
		if newFilename != filename {
			if err := RenameFile(notesDir, filename, newFilename); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			filename = newFilename
		}
	}

	if body.Content != nil {
		if err := WriteContent(notesDir, filename, *body.Content); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	if body.Category != nil {
		category = *body.Category
	}

	// Compute etag from current file content
	content, _ := ReadContent(notesDir, filename)
	etag := ComputeETag(content)

	if err := UpdateNoteMeta(db, id, filename, category, etag); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
	writeJSON(w, http.StatusOK, noteToAPI(note, notesDir, true))
}

func (a *NotesAPI) deleteNote(w http.ResponseWriter, db *sql.DB, notesDir string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	DeleteFile(notesDir, note.Filename)
	DeleteNoteMeta(db, id)
	w.WriteHeader(http.StatusOK)
}

func (a *NotesAPI) getSettings(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"notesPath": "Notes",
		"fileSuffix": ".md",
	})
}

func (a *NotesAPI) putSettings(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusOK, map[string]any{
		"notesPath": "Notes",
		"fileSuffix": ".md",
	})
}

// --- Helpers ---

func noteToAPI(n *Note, notesDir string, includeContent bool) map[string]any {
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
		content, _ := ReadContent(notesDir, n.Filename)
		m["content"] = content
	}
	return m
}

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

func pathID(path, prefix string) int64 {
	s := strings.TrimPrefix(path, prefix)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}
