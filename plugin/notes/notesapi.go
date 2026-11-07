package notes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io/fs"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
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
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}

	username := u.Username
	if err := a.plugin.ensureNotesDirFor(username); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
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
		a.deleteNote(w, r, db, username, id)
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
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}

	out := make([]map[string]any, len(notes))
	for i := range notes {
		if out[i], err = a.noteToAPI(&notes[i], username, exclude != "content"); err != nil {
			a.serverError(w, err)
			return
		}
	}
	plugin.WriteJSON(w, http.StatusOK, out)
}

func (a *NotesAPI) getNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		a.noteError(w, err)
		return
	}

	// If-None-Match support
	if match := r.Header.Get("If-None-Match"); match != "" && match == note.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	a.writeNote(w, http.StatusOK, note, username)
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

	filename, err := a.plugin.freeNoteFilename(db, username, body.Title, nil)
	if err != nil {
		a.serverError(w, err)
		return
	}
	etag := ComputeETag(body.Content)

	if err := a.plugin.writeNoteContent(username, filename, body.Content); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}

	note, err := CreateNoteMeta(db, filename, body.Category, etag)
	if err != nil {
		plugin.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}

	if body.Favorite {
		if err := ToggleFavorite(db, note.ID); err != nil {
			a.serverError(w, err)
			return
		}
		if note, err = GetNote(db, note.ID); err != nil {
			a.serverError(w, err)
			return
		}
	}

	a.writeNote(w, http.StatusOK, note, username)
}

func (a *NotesAPI) updateNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		a.noteError(w, err)
		return
	}

	// If-Match concurrency check
	if match := r.Header.Get("If-Match"); match != "" && match != note.ETag {
		a.writeNote(w, http.StatusPreconditionFailed, note, username)
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
		newFilename, err := a.plugin.freeNoteFilename(db, username, *body.Title, note)
		if err != nil {
			a.serverError(w, err)
			return
		}
		if newFilename != filename {
			if err := a.plugin.renameNoteFile(username, filename, newFilename); err != nil {
				httperr.Log(r, err)
				plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
				return
			}
			filename = newFilename
		}
	}

	if body.Content != nil {
		if err := a.plugin.writeNoteContent(username, filename, *body.Content); err != nil {
			httperr.Log(r, err)
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
			return
		}
	}

	if body.Category != nil {
		category = *body.Category
	}

	// Compute etag from current file content
	content, err := a.plugin.readNoteContent(username, filename)
	if err != nil {
		a.serverError(w, fmt.Errorf("read %s: %w", filename, err))
		return
	}
	etag := ComputeETag(content)

	if err := UpdateNoteMeta(db, id, filename, category, etag); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}

	// UpdateNoteMeta leaves the favourite alone, so note still has it.
	if body.Favorite != nil && note.Favorite != *body.Favorite {
		if err := ToggleFavorite(db, id); err != nil {
			a.serverError(w, err)
			return
		}
	}

	if note, err = GetNote(db, id); err != nil {
		a.noteError(w, err)
		return
	}
	a.writeNote(w, http.StatusOK, note, username)
}

func (a *NotesAPI) deleteNote(w http.ResponseWriter, r *http.Request, db *sql.DB, username string, id int64) {
	note, err := GetNote(db, id)
	if err != nil {
		a.noteError(w, err)
		return
	}
	if err := a.plugin.deleteNote(db, username, note); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
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
	// The settings are fixed; the body is read only to reject a malformed one.
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"notesPath":  "Notes",
		"fileSuffix": ".md",
	})
}

// --- Helpers ---

// writeNote answers with a note, its content included, and its ETag.
func (a *NotesAPI) writeNote(w http.ResponseWriter, code int, n *Note, username string) {
	out, err := a.noteToAPI(n, username, true)
	if err != nil {
		a.serverError(w, err)
		return
	}
	w.Header().Set("ETag", n.ETag)
	plugin.WriteJSON(w, code, out)
}

// noteError answers 404 for a note that does not exist and 500 otherwise.
func (a *NotesAPI) noteError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	a.serverError(w, err)
}

func (a *NotesAPI) serverError(w http.ResponseWriter, err error) {
	a.plugin.log.Error("notes api", "err", err)
	plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
}

// noteToAPI is a note as the Notes API shows it. A note whose file has gone
// (deleted through the files app) shows as empty rather than failing the
// whole listing.
func (a *NotesAPI) noteToAPI(n *Note, username string, includeContent bool) (map[string]any, error) {
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
		content, err := a.plugin.readNoteContent(username, n.Filename)
		if err != nil && !errors.Is(err, vfs.ErrNotFound) && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", n.Filename, err)
		}
		m["content"] = content
	}
	return m, nil
}
