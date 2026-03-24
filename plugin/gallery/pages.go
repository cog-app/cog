package gallery

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/ui"
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
	photoViewPage(r.URL.Path, m, tags, faces, plugin.CSRFToken(r)).Render(r.Context(), w)
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

// PersonView renders faces for a specific person.
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
	faces, _ := FacesByPerson(db, id, 200, 0)
	plugin.SetTitle(w, person.DisplayName())
	personViewPage(r.URL.Path, person, faces, plugin.CSRFToken(r)).Render(r.Context(), w)
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

// FaceThumbnail serves a cropped face thumbnail from the source media thumbnail.
func (h *Pages) FaceThumbnail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	faceID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	face, err := GetFaceWithMedia(db, faceID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, face.MediaID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	username := plugin.Username(r)
	thumbFile, err := h.plugin.PreviewPath(username, m, 1024)
	if err != nil {
		http.Error(w, "thumbnail unavailable", http.StatusNotFound)
		return
	}
	defer thumbFile.Close()

	img, _, err := image.Decode(thumbFile)
	if err != nil {
		http.Error(w, "decode error", http.StatusInternalServerError)
		return
	}

	// Scale normalized face coords to thumbnail pixel coords with padding.
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())
	fx := face.X * imgW
	fy := face.Y * imgH
	fw := face.W * imgW
	fh := face.H * imgH

	// Add 20% padding.
	padW := fw * 0.2
	padH := fh * 0.2
	x0 := int(max(fx-padW, 0))
	y0 := int(max(fy-padH, 0))
	x1 := int(min(fx+fw+padW, imgW))
	y1 := int(min(fy+fh+padH, imgH))

	cropped := cropImage(img, image.Rect(bounds.Min.X+x0, bounds.Min.Y+y0, bounds.Min.X+x1, bounds.Min.Y+y1))

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "face.jpg", time.Time{}, bytes.NewReader(buf.Bytes()))
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
	// If merge_into is set, merge this person into the target.
	if mergeInto := r.FormValue("merge_into"); mergeInto != "" {
		targetID, err := strconv.ParseInt(mergeInto, 10, 64)
		if err != nil {
			http.Error(w, "invalid merge target", http.StatusBadRequest)
			return
		}
		if err := MergePeopleRows(db, targetID, id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", targetID), http.StatusSeeOther)
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

// RemoveFaceFromPerson moves a face to a new person so it can be merged elsewhere.
func (h *Pages) RemoveFaceFromPerson(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	personID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	faceID, err := strconv.ParseInt(r.FormValue("face_id"), 10, 64)
	if err != nil {
		http.Error(w, "face_id required", http.StatusBadRequest)
		return
	}
	if _, err := CreatePersonFromFace(db, faceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", personID), http.StatusSeeOther)
}

// AddMediaTagAction adds a tag to a media item.
func (h *Pages) AddMediaTagAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tagName := strings.TrimSpace(r.FormValue("tag_name"))
	if tagName == "" {
		http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
		return
	}
	tagID, err := UpsertTag(db, tagName, 0)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := AddMediaTag(db, mediaID, tagID, 1.0); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// RemoveMediaTagAction removes a tag from a media item.
func (h *Pages) RemoveMediaTagAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	tagID, err := strconv.ParseInt(r.FormValue("tag_id"), 10, 64)
	if err != nil {
		http.Error(w, "tag_id required", http.StatusBadRequest)
		return
	}
	if err := RemoveMediaTag(db, mediaID, tagID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// DeleteFaceAction deletes a face detection from a media item.
func (h *Pages) DeleteFaceAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	faceID, err := strconv.ParseInt(r.FormValue("face_id"), 10, 64)
	if err != nil {
		http.Error(w, "face_id required", http.StatusBadRequest)
		return
	}
	if err := DeleteFace(db, faceID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// CreatePersonFromFaceAction creates a new person from a face and redirects to the new person.
func (h *Pages) CreatePersonFromFaceAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	faceID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	personID, err := CreatePersonFromFace(db, faceID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", personID), http.StatusSeeOther)
}

// SearchTagsAPI returns tag search results as picker options.
func (h *Pages) SearchTagsAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("q")
	tags, _ := SearchTags(db, q, 10)
	items := make([]ui.PickerItem, len(tags))
	for i, t := range tags {
		items[i] = ui.PickerItem{ID: t.Name, Name: t.Name}
	}
	ui.PickerResults(items).Render(r.Context(), w)
}

// SearchPeopleAPI returns people search results as picker options.
func (h *Pages) SearchPeopleAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query().Get("name")
	if q == "" {
		q = r.URL.Query().Get("q")
	}
	excludeID, _ := strconv.ParseInt(r.URL.Query().Get("exclude"), 10, 64)
	people, _ := SearchPeople(db, q, excludeID, 10)
	items := make([]ui.PickerItem, len(people))
	for i, p := range people {
		items[i] = ui.PickerItem{
			ID:   strconv.FormatInt(p.ID, 10),
			Name: p.DisplayName(),
			Sub:  fmt.Sprintf("%d faces", p.FaceCount),
		}
		if p.BestFaceID != nil {
			items[i].AvatarURL = fmt.Sprintf("/apps/gallery/api/face/%d/thumbnail", *p.BestFaceID)
		}
	}
	ui.PickerResults(items).Render(r.Context(), w)
}

// TriggerScan manually triggers a media scan via the scan worker subprocess.
func (h *Pages) TriggerScan(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	go h.plugin.requestScan(username)
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

// UserScanStatusPartial returns a small scan-running banner for the gallery sidebar.
func (h *Pages) UserScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	scan := h.plugin.GetScanStatus()
	userScanBanner(scan).Render(r.Context(), w)
}

// TriggerSystemScan triggers a full scan of all users from the admin page.
func (h *Pages) TriggerSystemScan(w http.ResponseWriter, r *http.Request) {
	go h.plugin.requestScan("*")
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

	// Each settings card is a separate form with a _section hidden field.
	// Only save settings for the submitted section to avoid overwriting
	// checkbox values from the other form (unchecked checkboxes are absent).
	switch r.FormValue("_section") {
	case "tags":
		SetSetting(db, "tag_backend", r.FormValue("tag_backend"))
		if v := r.FormValue("tag_min_confidence"); v != "" {
			SetSetting(db, "tag_min_confidence", v)
		}
	case "faces":
		SetSetting(db, "face_backend", r.FormValue("face_backend"))
		if v := r.FormValue("face_min_confidence"); v != "" {
			SetSetting(db, "face_min_confidence", v)
		}
		if v := r.FormValue("cluster_threshold"); v != "" {
			SetSetting(db, "cluster_threshold", v)
		}
	case "duplicates":
		SetSetting(db, "duplicate_detection_enabled", r.FormValue("duplicate_detection_enabled"))
		if v := r.FormValue("duplicate_algorithm"); v != "" {
			SetSetting(db, "duplicate_algorithm", v)
		}
		if v := r.FormValue("duplicate_distance"); v != "" {
			SetSetting(db, "duplicate_distance", v)
		}
	case "ocr":
		SetSetting(db, "ocr_enabled", r.FormValue("ocr_enabled"))
	case "processing":
		SetSetting(db, "preview_generation_enabled", r.FormValue("preview_generation_enabled"))
		SetSetting(db, "quality_scoring_enabled", r.FormValue("quality_scoring_enabled"))
	case "backend":
		SetSetting(db, "onnx_library_path", r.FormValue("onnx_library_path"))
		SetSetting(db, "onnx_models_dir", r.FormValue("onnx_models_dir"))
		SetSetting(db, "recognition_url", r.FormValue("recognition_url"))
		SetSetting(db, "recognition_api_key", r.FormValue("recognition_api_key"))
		SetSetting(db, "recognition_model", r.FormValue("recognition_model"))
	}

	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// --- Duplicate handlers ---

// Duplicates renders the duplicate groups list.
func (h *Pages) Duplicates(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groups, _ := ListDuplicateGroups(db)
	plugin.SetTitle(w, "Duplicates")
	duplicatesPage(r.URL.Path, groups).Render(r.Context(), w)
}

// DuplicateGroupView renders a single duplicate group.
func (h *Pages) DuplicateGroupView(w http.ResponseWriter, r *http.Request) {
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
	members, err := DuplicateGroupMembers(db, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	plugin.SetTitle(w, "Duplicate Group")
	duplicateGroupViewPage(r.URL.Path, id, members, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// SetDuplicateKeep sets the keep item in a duplicate group.
func (h *Pages) SetDuplicateKeep(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mediaID, err := strconv.ParseInt(r.FormValue("media_id"), 10, 64)
	if err != nil {
		http.Error(w, "media_id required", http.StatusBadRequest)
		return
	}
	if err := SetKeepMember(db, groupID, mediaID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/duplicates/%d", groupID), http.StatusSeeOther)
}

// UnmarkDuplicate removes a member from a duplicate group.
func (h *Pages) UnmarkDuplicate(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mediaID, err := strconv.ParseInt(r.FormValue("media_id"), 10, 64)
	if err != nil {
		http.Error(w, "media_id required", http.StatusBadRequest)
		return
	}
	if err := RemoveDuplicateMember(db, groupID, mediaID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Delete group if fewer than 2 members remain.
	count, _ := DuplicateGroupMemberCount(db, groupID)
	if count < 2 {
		DeleteDuplicateGroup(db, groupID)
		http.Redirect(w, r, "/apps/gallery/duplicates", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/duplicates/%d", groupID), http.StatusSeeOther)
}

// DeleteNonKeepDuplicates deletes non-keep files in a duplicate group.
func (h *Pages) DeleteNonKeepDuplicates(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	username := plugin.Username(r)
	members, err := DuplicateGroupMembers(db, groupID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, m := range members {
		if m.IsKeep {
			continue
		}
		if err := h.plugin.files.Delete(username, m.Media.FilePath); err != nil {
			h.plugin.log.Warn("delete duplicate", "path", m.Media.FilePath, "err", err)
		}
		DeleteMediaByFileID(db, m.Media.FileID)
	}
	DeleteDuplicateGroup(db, groupID)
	http.Redirect(w, r, "/apps/gallery/duplicates", http.StatusSeeOther)
}

// DeleteAllNonKeep deletes non-keep files across all duplicate groups.
func (h *Pages) DeleteAllNonKeep(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	username := plugin.Username(r)
	groups, err := ListDuplicateGroups(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for _, g := range groups {
		members, err := DuplicateGroupMembers(db, g.ID)
		if err != nil {
			continue
		}
		for _, m := range members {
			if m.IsKeep {
				continue
			}
			if err := h.plugin.files.Delete(username, m.Media.FilePath); err != nil {
				h.plugin.log.Warn("delete duplicate", "path", m.Media.FilePath, "err", err)
			}
			DeleteMediaByFileID(db, m.Media.FileID)
		}
		DeleteDuplicateGroup(db, g.ID)
	}
	http.Redirect(w, r, "/apps/gallery/duplicates", http.StatusSeeOther)
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
