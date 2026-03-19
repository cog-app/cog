package gallery

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages handles the gallery web UI routes.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

// Timeline renders the main photo timeline view.
func (h *Pages) Timeline(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	days, _ := TimelineDays(db)
	media, _ := RecentMedia(db, 100)
	total, _ := MediaCount(db)
	plugin.SetTitle(w, "Gallery")
	timelinePage(r.URL.Path, days, media, total).Render(r.Context(), w)
}

// Albums renders the album list view.
func (h *Pages) Albums(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	albums, _ := ListAlbums(db, -1)
	plugin.SetTitle(w, "Albums")
	albumsPage(r.URL.Path, albums).Render(r.Context(), w)
}

// AlbumView renders a single album.
func (h *Pages) AlbumView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	plugin.SetTitle(w, album.Name)
	albumViewPage(r.URL.Path, album, media, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// PhotoView renders the detail view for a single photo.
func (h *Pages) PhotoView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	faces, _ := FacesForMedia(db, id)
	plugin.SetTitle(w, m.FileName())
	photoViewPage(r.URL.Path, m, tags, faces).Render(r.Context(), w)
}

// People renders the people/faces view.
func (h *Pages) People(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	people, _ := ListPeople(db)
	plugin.SetTitle(w, "People")
	peoplePage(r.URL.Path, people).Render(r.Context(), w)
}

// PersonView renders media for a specific person.
func (h *Pages) PersonView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	mediaIDs := make([]int64, len(media))
	for i := range media {
		mediaIDs[i] = media[i].ID
	}
	facesMap, _ := FacesForMediaBatch(db, mediaIDs)
	overlays := make(map[int64][]FaceOverlay)
	for _, m := range media {
		for _, f := range facesMap[m.ID] {
			if m.IsVideo() || m.Width == 0 || m.Height == 0 {
				continue
			}
			o := TransformFaceForCover(f, m.Width, m.Height, id)
			overlays[m.ID] = append(overlays[m.ID], o)
		}
	}
	plugin.SetTitle(w, person.DisplayName())
	personViewPage(r.URL.Path, person, media, overlays, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// Tags renders the tag browser.
func (h *Pages) Tags(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tags, _ := ListTags(db)
	plugin.SetTitle(w, "Tags")
	tagsPage(r.URL.Path, tags).Render(r.Context(), w)
}

// TagView renders media for a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	media, _ := MediaByTag(db, name, 200, 0)
	plugin.SetTitle(w, "Tag: "+name)
	tagViewPage(r.URL.Path, name, media).Render(r.Context(), w)
}

// Videos renders the video-only timeline.
func (h *Pages) Videos(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	media, _ := VideoMedia(db, 100, 0)
	plugin.SetTitle(w, "Videos")
	videosPage(media).Render(r.Context(), w)
}

// Settings renders the gallery settings page.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	total, _ := MediaCount(db)
	plugin.SetTitle(w, "Gallery Settings")
	settingsPage(db, total, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// --- API handlers ---

// TimelineAPI returns JSON timeline data for virtual scrolling.
func (h *Pages) TimelineAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	days, err := TimelineDays(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, days)
}

// MediaDetailAPI returns full media detail as JSON.
func (h *Pages) MediaDetailAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	plugin.WriteJSON(w, http.StatusOK, m)
}

// MediaThumbnail serves a thumbnail for a media item.
func (h *Pages) MediaThumbnail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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

	username := plugin.Username(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	db, err := plugin.UserDB(r)
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
	username := plugin.Username(r)
	// Use a detached context — the request context is cancelled when the redirect completes.
	go h.plugin.ScanUser(context.Background(), username)
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}

// SaveRecognitionSettings saves recognition backend settings (legacy per-user, redirects to system settings).
func (h *Pages) SaveRecognitionSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// SystemSettings renders the system-level gallery settings page (admin only).
func (h *Pages) SystemSettings(w http.ResponseWriter, r *http.Request) {
	sysDB, err := h.plugin.SystemDB()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stats := h.plugin.AggregateRecognitionStats()
	scanStatus := h.plugin.GetScanStatus()
	plugin.SetTitle(w, "Gallery Settings")
	systemSettingsPage(sysDB, stats, scanStatus, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ScanStatusPartial returns the scan status card as an HTMX partial.
func (h *Pages) ScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	stats := h.plugin.AggregateRecognitionStats()
	scan := h.plugin.GetScanStatus()
	scanStatusCard(stats, scan, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// TriggerSystemScan triggers a full scan of all users from the admin page.
func (h *Pages) TriggerSystemScan(w http.ResponseWriter, r *http.Request) {
	go h.plugin.ScanAllUsers(context.Background())
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// PurgeRecognitions purges all recognition data and resets state for all users.
func (h *Pages) PurgeRecognitions(w http.ResponseWriter, r *http.Request) {
	if err := h.plugin.PurgeAllRecognitions(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// SaveSystemSettings saves system-level gallery settings.
func (h *Pages) SaveSystemSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.SystemDB()
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
	if v := r.FormValue("cluster_threshold"); v != "" {
		SetSetting(db, "cluster_threshold", v)
	}
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// RenamePreview shows a preview of the rename operation.
func (h *Pages) RenamePreview(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	plugin.SetTitle(w, "Rename Preview")
	renamePreviewPage(pattern, previews, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// RenameApply executes the rename operation.
func (h *Pages) RenameApply(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
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
	username := plugin.Username(r)
	if err := h.plugin.ApplyRename(username, previews); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}

// RenameUndo reverses the most recent rename batch.
func (h *Pages) RenameUndo(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	if err := h.plugin.UndoLastRename(username); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
}
