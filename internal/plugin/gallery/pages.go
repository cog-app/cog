package gallery

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/ui"
)

// Pages handles the gallery web UI routes.
type Pages struct {
	plugin *Plugin
	secret []byte
	nav    dbProvider
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin, secret []byte, nav dbProvider) *Pages {
	return &Pages{plugin: p, secret: secret, nav: nav}
}

func (h *Pages) userDB(r *http.Request) (*sql.DB, error) {
	u := auth.UserFromContext(r.Context())
	return h.plugin.DB(u.Username)
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

func (h *Pages) username(r *http.Request) string {
	return auth.UserFromContext(r.Context()).Username
}

// Timeline renders the main photo timeline view.
func (h *Pages) Timeline(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	days, _ := TimelineDays(db)
	media, _ := RecentMedia(db, 100)
	total, _ := MediaCount(db)
	pd := h.pageData(r, "Gallery")
	timelinePage(pd, days, media, total).Render(r.Context(), w)
}

// Albums renders the album list view.
func (h *Pages) Albums(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	albums, _ := ListAlbums(db, -1)
	pd := h.pageData(r, "Albums")
	albumsPage(pd, albums).Render(r.Context(), w)
}

// AlbumView renders a single album.
func (h *Pages) AlbumView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	album, err := GetAlbum(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	media, _ := AlbumMedia(db, id, 200, 0)
	pd := h.pageData(r, album.Name)
	albumViewPage(pd, album, media).Render(r.Context(), w)
}

// PhotoView renders the detail view for a single photo.
func (h *Pages) PhotoView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tags, _ := TagsForMedia(db, id)
	pd := h.pageData(r, m.FileName())
	photoViewPage(pd, m, tags).Render(r.Context(), w)
}

// People renders the people/faces view.
func (h *Pages) People(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	people, _ := ListPeople(db)
	pd := h.pageData(r, "People")
	peoplePage(pd, people).Render(r.Context(), w)
}

// PersonView renders media for a specific person.
func (h *Pages) PersonView(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	person, err := GetPerson(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	media, _ := MediaByPerson(db, id, 200, 0)
	pd := h.pageData(r, person.DisplayName())
	personViewPage(pd, person, media).Render(r.Context(), w)
}

// Tags renders the tag browser.
func (h *Pages) Tags(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, _ := ListTags(db)
	pd := h.pageData(r, "Tags")
	tagsPage(pd, tags).Render(r.Context(), w)
}

// Videos renders the video-only timeline.
func (h *Pages) Videos(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	media, _ := VideoMedia(db, 100, 0)
	pd := h.pageData(r, "Videos")
	videosPage(pd, media).Render(r.Context(), w)
}

// Settings renders the gallery settings page.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, _ := MediaCount(db)
	pd := h.pageData(r, "Gallery Settings")
	settingsPage(pd, db, total).Render(r.Context(), w)
}

// --- API handlers ---

// TimelineAPI returns JSON timeline data for virtual scrolling.
func (h *Pages) TimelineAPI(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	days, err := TimelineDays(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, days)
}

// MediaDetailAPI returns full media detail as JSON.
func (h *Pages) MediaDetailAPI(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, m)
}

// MediaThumbnail serves a thumbnail for a media item.
func (h *Pages) MediaThumbnail(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	size := 256
	if s := r.URL.Query().Get("size"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			size = v
		}
	}

	username := h.username(r)
	f, err := h.plugin.PreviewPath(username, m, size)
	if err != nil {
		http.Error(w, "thumbnail unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "thumbnail.jpg", time.Time{}, f)
}

// --- Action handlers ---

// CreateAlbum creates a new manual album.
func (h *Pages) CreateAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	id, err := CreateAlbumRow(db, name, r.FormValue("description"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/album/%d", id), http.StatusSeeOther)
}

// AddToAlbum adds a media item to an album.
func (h *Pages) AddToAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	albumID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mediaID, err := strconv.ParseInt(r.FormValue("media_id"), 10, 64)
	if err != nil {
		http.Error(w, "media_id required", http.StatusBadRequest)
		return
	}
	if err := AddMediaToAlbum(db, albumID, mediaID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RemoveFromAlbum removes a media item from an album.
func (h *Pages) RemoveFromAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	albumID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("media_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := RemoveMediaFromAlbum(db, albumID, mediaID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// DeleteAlbum deletes an album.
func (h *Pages) DeleteAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := DeleteAlbumRow(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/albums", http.StatusSeeOther)
}

// NamePerson names a recognized person.
func (h *Pages) NamePerson(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name := r.FormValue("name")
	if err := NamePersonRow(db, id, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", id), http.StatusSeeOther)
}

// MergePeople merges two people.
func (h *Pages) MergePeople(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	targetID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	sourceID, err := strconv.ParseInt(r.FormValue("source_id"), 10, 64)
	if err != nil {
		http.Error(w, "source_id required", http.StatusBadRequest)
		return
	}
	if err := MergePeopleRows(db, targetID, sourceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", targetID), http.StatusSeeOther)
}

// HidePerson hides a person from the people view.
func (h *Pages) HidePerson(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := HidePersonRow(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/people", http.StatusSeeOther)
}

// TriggerScan manually triggers a media scan.
func (h *Pages) TriggerScan(w http.ResponseWriter, r *http.Request) {
	username := h.username(r)
	// Use a detached context — the request context is cancelled when the redirect completes.
	go h.plugin.ScanUser(context.Background(), username)
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}

// SaveRecognitionSettings saves recognition backend settings.
func (h *Pages) SaveRecognitionSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	SetSetting(db, "recognition_enabled", r.FormValue("recognition_enabled"))
	SetSetting(db, "recognition_backend", r.FormValue("recognition_backend"))
	SetSetting(db, "recognition_url", r.FormValue("recognition_url"))
	SetSetting(db, "recognition_api_key", r.FormValue("recognition_api_key"))
	SetSetting(db, "recognition_model", r.FormValue("recognition_model"))
	SetSetting(db, "onnx_library_path", r.FormValue("onnx_library_path"))
	SetSetting(db, "onnx_models_dir", r.FormValue("onnx_models_dir"))
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}

// RenamePreview shows a preview of the rename operation.
func (h *Pages) RenamePreview(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pattern := r.FormValue("pattern")
	if pattern == "" {
		http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
		return
	}
	previews, err := PreviewRename(db, pattern)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pd := h.pageData(r, "Rename Preview")
	renamePreviewPage(pd, pattern, previews).Render(r.Context(), w)
}

// RenameApply executes the rename operation.
func (h *Pages) RenameApply(w http.ResponseWriter, r *http.Request) {
	db, err := h.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	pattern := r.FormValue("pattern")
	previews, err := PreviewRename(db, pattern)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	username := h.username(r)
	if err := h.plugin.ApplyRename(username, previews); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}

// RenameUndo reverses the most recent rename batch.
func (h *Pages) RenameUndo(w http.ResponseWriter, r *http.Request) {
	username := h.username(r)
	if err := h.plugin.UndoLastRename(username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}
