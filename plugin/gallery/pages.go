package gallery

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"image"
	"image/jpeg"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
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

// isAdmin reports whether the request comes from an administrator, who can
// reach the gallery's system settings.
func isAdmin(r *http.Request) bool {
	u := plugin.UserFromContext(r.Context())
	return u != nil && u.IsAdmin
}

// serverError reports a failure the user cannot fix and logs it.
func (h *Pages) serverError(w http.ResponseWriter, r *http.Request, err error) {
	h.plugin.log.Error("gallery page", "path", r.URL.Path, "err", err)
	httperr.Internal(w, r, err)
}

// flashRedirect leaves a message for the page at target and sends the
// browser there.
func flashRedirect(w http.ResponseWriter, r *http.Request, kind, msg, target string) {
	plugin.SetFlash(w, plugin.Flash{Kind: kind, Message: msg})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// photoDay is one day of the timeline: its heading and its photos.
type photoDay struct {
	Label string
	Media []Media
}

// groupByDay splits media, newest first, into days. Days are in UTC, the
// same as the timeline API's day IDs.
func groupByDay(media []Media) []photoDay {
	var days []photoDay
	for _, m := range media {
		label := takenAt(&m).Format("Monday, January 2, 2006")
		if n := len(days); n > 0 && days[n-1].Label == label {
			days[n-1].Media = append(days[n-1].Media, m)
			continue
		}
		days = append(days, photoDay{Label: label, Media: []Media{m}})
	}
	return days
}

// Timeline renders the main photo timeline view, grouped by day.
func (h *Pages) Timeline(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	media, err := RecentMedia(db, 100)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Gallery")
	ui.Render(w, r, timelinePage(r.URL.Path, groupByDay(media)))
}

// Albums renders the album list view.
func (h *Pages) Albums(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	albums, err := ListAlbums(db, -1)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Albums")
	ui.Render(w, r, albumsPage(r.URL.Path, albums, plugin.CSRFToken(r)))
}

// AlbumView renders a single album.
func (h *Pages) AlbumView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	album, err := GetAlbum(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	media, err := AlbumMedia(db, id, 200, 0)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	// A manual album offers the recent photos it does not have yet to add.
	var candidates []Media
	if album.AlbumType == AlbumManual {
		recent, err := RecentMedia(db, 200)
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		in := make(map[int64]bool, len(media))
		for _, m := range media {
			in[m.ID] = true
		}
		for _, m := range recent {
			if !in[m.ID] {
				candidates = append(candidates, m)
			}
		}
	}
	plugin.SetTitle(w, album.Name)
	ui.Render(w, r, albumViewPage(r.URL.Path, album, media, candidates, plugin.CSRFToken(r)))
}

// PhotoView renders the detail view for a single photo.
func (h *Pages) PhotoView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	tags, err := TagsForMedia(db, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	faces, err := FacesForMedia(db, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	albums, err := ListAlbums(db, AlbumManual)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	newer, older, err := NeighbourMedia(db, m)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	isFavorite := false
	if h.plugin.files != nil {
		filesDB, err := h.plugin.ctx.PluginDB(plugin.Username(r), plugin.OfPlugin("files"))
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		var fav int
		err = filesDB.QueryRow(`SELECT favorite FROM files WHERE id = ?`, m.FileID).Scan(&fav)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			h.serverError(w, r, err)
			return
		}
		isFavorite = fav == 1
	}
	plugin.SetTitle(w, m.FileName())
	ui.Render(w, r, photoViewPage(photoView{
		Media:    m,
		Tags:     tags,
		Faces:    faces,
		Albums:   albums,
		Newer:    newer,
		Older:    older,
		Favorite: isFavorite,
	}, plugin.CSRFToken(r)))
}

// People renders the people/faces view.
func (h *Pages) People(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	people, err := ListPeople(db)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	unnamed := make([]Person, 0, len(people))
	for _, p := range people {
		if p.Name == "" {
			unnamed = append(unnamed, p)
		}
	}
	unnamedOnly := r.URL.Query().Get("filter") == "unnamed"
	shown := people
	if unnamedOnly {
		shown = unnamed
	}
	plugin.SetTitle(w, "People")
	ui.Render(w, r, peoplePage(r.URL.Path, shown, len(people), len(unnamed), unnamedOnly, isAdmin(r)))
}

// PersonView renders faces for a specific person.
func (h *Pages) PersonView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	person, err := GetPerson(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	faces, err := FacesByPerson(db, id, 200, 0)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, person.DisplayName())
	ui.Render(w, r, personViewPage(r.URL.Path, person, faces, plugin.CSRFToken(r)))
}

// Tags renders the tag browser.
func (h *Pages) Tags(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	tags, err := ListTags(db)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Tags")
	ui.Render(w, r, tagsPage(r.URL.Path, tags, isAdmin(r)))
}

// TagView renders media for a specific tag.
func (h *Pages) TagView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	name := r.PathValue("name")
	media, err := MediaByTag(db, name, 200, 0)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Tag: "+name)
	ui.Render(w, r, tagViewPage(r.URL.Path, name, media))
}

// Videos renders the video-only timeline.
func (h *Pages) Videos(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	media, err := VideoMedia(db, 100, 0)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Videos")
	ui.Render(w, r, videosPage(media))
}

// Settings renders the gallery settings page.
func (h *Pages) Settings(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	total, err := MediaCount(db)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Gallery Settings")
	ui.Render(w, r, settingsPage(db, total, plugin.CSRFToken(r)))
}

// --- API handlers ---

// TimelineAPI returns JSON timeline data for virtual scrolling.
func (h *Pages) TimelineAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	days, err := TimelineDays(db)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, days)
}

// MediaDetailAPI returns full media detail as JSON.
func (h *Pages) MediaDetailAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, m)
}

// MediaThumbnail serves a thumbnail for a media item.
func (h *Pages) MediaThumbnail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
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
		h.plugin.log.Warn("thumbnail", "media", m.ID, "err", err)
		http.Error(w, "thumbnail unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "thumbnail.jpg", time.Time{}, f)
}

// FaceThumbnail serves a cropped face thumbnail from the source media thumbnail.
func (h *Pages) FaceThumbnail(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	faceID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	face, err := GetFaceWithMedia(db, faceID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	m, err := GetMedia(db, face.MediaID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}

	username := plugin.Username(r)
	thumbFile, err := h.plugin.PreviewPath(username, m, 1024)
	if err != nil {
		h.plugin.log.Warn("face thumbnail", "media", m.ID, "err", err)
		http.Error(w, "thumbnail unavailable", http.StatusNotFound)
		return
	}
	defer thumbFile.Close()

	img, _, err := image.Decode(thumbFile)
	if err != nil {
		h.plugin.log.Error("face thumbnail: decode", "media", m.ID, "err", err)
		http.Error(w, "decode error", http.StatusInternalServerError)
		return
	}

	cropped := cropFace(img, &face.Face)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, cropped, &jpeg.Options{Quality: 85}); err != nil {
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "face.jpg", time.Time{}, bytes.NewReader(buf.Bytes()))
}

// --- Action handlers ---

// CreateAlbum creates a new manual album.
func (h *Pages) CreateAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		flashRedirect(w, r, "error", "An album needs a name.", "/apps/gallery/albums")
		return
	}
	id, err := CreateAlbumRow(db, name, strings.TrimSpace(r.FormValue("description")))
	if err != nil {
		flashRedirect(w, r, "error", "The album could not be created: "+err.Error(), "/apps/gallery/albums")
		return
	}
	flashRedirect(w, r, "success", "Created “"+name+"”.", fmt.Sprintf("/apps/gallery/album/%d", id))
}

// galleryReturn is where a form asked to go back to, if it is a gallery
// page; otherwise fallback. It keeps a posted "return" from sending the
// browser off-site.
func galleryReturn(r *http.Request, fallback string) string {
	ret := r.FormValue("return")
	if strings.HasPrefix(ret, "/apps/gallery/") && !strings.HasPrefix(ret, "//") {
		return ret
	}
	return fallback
}

// AddToAlbum adds one or more media items (media_id, repeated) to an album.
func (h *Pages) AddToAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	albumID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	album, err := GetAlbum(db, albumID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	back := galleryReturn(r, fmt.Sprintf("/apps/gallery/album/%d", albumID))
	if err := r.ParseForm(); err != nil {
		flashRedirect(w, r, "error", "Nothing was added: "+err.Error(), back)
		return
	}
	ids := r.PostForm["media_id"]
	if len(ids) == 0 {
		flashRedirect(w, r, "error", "Choose at least one photo to add.", back)
		return
	}
	for _, v := range ids {
		mediaID, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			flashRedirect(w, r, "error", "Nothing was added: invalid photo.", back)
			return
		}
		if err := AddMediaToAlbum(db, albumID, mediaID); err != nil {
			flashRedirect(w, r, "error", "The photos could not be added: "+err.Error(), back)
			return
		}
	}
	flashRedirect(w, r, "success", fmt.Sprintf("Added %s to “%s”.", plural(len(ids), "photo", "photos"), album.Name), back)
}

// RemoveFromAlbum removes a media item from an album.
func (h *Pages) RemoveFromAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
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
		h.serverError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// albumPhoto reads the album named by the path and the photo named by the
// form's media_id, for the album page's tile menu. On failure it has
// answered: 404 for an album that does not exist, a flash for a bad photo.
func (h *Pages) albumPhoto(w http.ResponseWriter, r *http.Request, db *sql.DB) (*Album, *Media, bool) {
	albumID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, nil, false
	}
	album, err := GetAlbum(db, albumID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		h.serverError(w, r, err)
		return nil, nil, false
	}
	back := fmt.Sprintf("/apps/gallery/album/%d", albumID)
	mediaID, err := strconv.ParseInt(r.FormValue("media_id"), 10, 64)
	if err != nil {
		flashRedirect(w, r, "error", "Choose a photo from the album.", back)
		return nil, nil, false
	}
	m, err := GetMedia(db, mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		flashRedirect(w, r, "error", "That photo is no longer in your gallery.", back)
		return nil, nil, false
	}
	if err != nil {
		h.serverError(w, r, err)
		return nil, nil, false
	}
	return album, m, true
}

// SetCover makes a photo of the album its cover.
func (h *Pages) SetCover(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	album, m, ok := h.albumPhoto(w, r, db)
	if !ok {
		return
	}
	back := fmt.Sprintf("/apps/gallery/album/%d", album.ID)
	err = SetAlbumCover(db, album.ID, m.ID)
	if errors.Is(err, ErrNotInAlbum) {
		flashRedirect(w, r, "error", "“"+m.FileName()+"” is not in “"+album.Name+"”, so it cannot be its cover.", back)
		return
	}
	if err != nil {
		flashRedirect(w, r, "error", "The cover could not be changed: "+err.Error(), back)
		return
	}
	flashRedirect(w, r, "success", "“"+m.FileName()+"” is now the cover of “"+album.Name+"”.", back)
}

// RemoveFromAlbumForm takes a photo out of a manual album from the album
// page's tile menu (PRG). The photo stays in the gallery; if it was the
// cover, the album goes back to its first photo.
func (h *Pages) RemoveFromAlbumForm(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	album, m, ok := h.albumPhoto(w, r, db)
	if !ok {
		return
	}
	back := fmt.Sprintf("/apps/gallery/album/%d", album.ID)
	if album.AlbumType != AlbumManual {
		flashRedirect(w, r, "error", "Photos cannot be taken out of an automatic album.", back)
		return
	}
	if err := RemoveMediaFromAlbum(db, album.ID, m.ID); err != nil {
		flashRedirect(w, r, "error", "“"+m.FileName()+"” could not be removed: "+err.Error(), back)
		return
	}
	flashRedirect(w, r, "success", "Removed “"+m.FileName()+"” from “"+album.Name+"”.", back)
}

// RenameAlbum changes a manual album's name and description.
func (h *Pages) RenameAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	back := fmt.Sprintf("/apps/gallery/album/%d", id)
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		flashRedirect(w, r, "error", "An album needs a name.", back)
		return
	}
	if err := RenameAlbumRow(db, id, name, strings.TrimSpace(r.FormValue("description"))); err != nil {
		flashRedirect(w, r, "error", "The album could not be renamed: "+err.Error(), back)
		return
	}
	flashRedirect(w, r, "success", "Saved “"+name+"”.", back)
}

// DeleteAlbum deletes an album (DELETE, or POST from the album page's
// confirmation dialog). Its photos stay in the gallery.
func (h *Pages) DeleteAlbum(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	album, err := GetAlbum(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if err := DeleteAlbumRow(db, id); err != nil {
		flashRedirect(w, r, "error", "The album could not be deleted: "+err.Error(), fmt.Sprintf("/apps/gallery/album/%d", id))
		return
	}
	flashRedirect(w, r, "success", "Deleted the album “"+album.Name+"”. Its photos are still in your gallery.", "/apps/gallery/albums")
}

// DeletePhoto moves a photo's file to the trash and drops it from the
// gallery.
func (h *Pages) DeletePhoto(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := GetMedia(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	back := fmt.Sprintf("/apps/gallery/photo/%d", id)
	if h.plugin.files == nil {
		flashRedirect(w, r, "error", "The photo could not be deleted: the files app is not available.", back)
		return
	}
	if err := h.plugin.files.Delete(plugin.Username(r), m.FilePath); err != nil {
		flashRedirect(w, r, "error", "The photo could not be deleted: "+err.Error(), back)
		return
	}
	if err := DeleteMediaByFileID(db, m.FileID); err != nil {
		h.serverError(w, r, err)
		return
	}
	flashRedirect(w, r, "success", "Moved “"+m.FileName()+"” to the trash.", "/apps/gallery/")
}

// NamePerson names a recognized person.
func (h *Pages) NamePerson(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
			httperr.Internal(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", targetID), http.StatusSeeOther)
		return
	}

	username := plugin.Username(r)

	// If contact_uid is set, link this person to an existing contact.
	if contactUID := r.FormValue("contact_uid"); contactUID != "" && h.plugin.contacts != nil {
		cs, err := h.plugin.contacts.Store(username)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		card, err := cs.FindByUID(contactUID)
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "contact not found", http.StatusNotFound)
			return
		}
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if err := LinkPersonToContact(db, id, contactUID); err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if err := NamePersonRow(db, id, card.FN); err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if !card.HasPhoto() {
			h.setContactPhotoFromFace(username, contactUID, db, id)
		}
		http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", id), http.StatusSeeOther)
		return
	}

	// Plain name set — also link/create a contact if the contacts plugin is available.
	name := r.FormValue("name")
	if err := NamePersonRow(db, id, name); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if name != "" && h.plugin.contacts != nil {
		h.linkOrCreateContact(username, db, id, name)
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", id), http.StatusSeeOther)
}

// MergePeople merges two people.
func (h *Pages) MergePeople(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", targetID), http.StatusSeeOther)
}

// linkOrCreateContact links a gallery person to an existing contact by exact name match,
// or creates a new contact if none exists. Errors are logged but not fatal.
func (h *Pages) linkOrCreateContact(username string, galleryDB *sql.DB, personID int64, name string) {
	cs, err := h.plugin.contacts.Store(username)
	if err != nil {
		h.plugin.log.Error("contacts store", "user", username, "err", err)
		return
	}
	// Search for an exact name match.
	cards, err := cs.Search(nil, name, 10)
	if err != nil {
		h.plugin.log.Error("search contacts", "err", err)
		return
	}
	for _, card := range cards {
		if !strings.EqualFold(card.FN, name) {
			continue
		}
		// Found an exact match — link to it.
		if err := LinkPersonToContact(galleryDB, personID, card.UID); err != nil {
			h.plugin.log.Error("link person to contact", "err", err)
		}
		if !card.HasPhoto() {
			h.setContactPhotoFromFace(username, card.UID, galleryDB, personID)
		}
		return
	}
	// No exact match — create a new contact.
	uid, err := h.plugin.contacts.CreateCardByName(username, name)
	if err != nil {
		h.plugin.log.Error("create contact from person", "err", err)
		return
	}
	if err := LinkPersonToContact(galleryDB, personID, uid); err != nil {
		h.plugin.log.Error("link person to new contact", "err", err)
	}
	h.setContactPhotoFromFace(username, uid, galleryDB, personID)
}

// setContactPhotoFromFace sets a contact's photo from the person's best face thumbnail.
// Errors are logged but not fatal.
func (h *Pages) setContactPhotoFromFace(username, contactUID string, galleryDB *sql.DB, personID int64) {
	if h.plugin.contacts == nil {
		return
	}
	if err := h.contactPhotoFromFace(username, contactUID, galleryDB, personID); err != nil {
		h.plugin.log.Error("set contact photo from face", "person", personID, "err", err)
	}
}

func (h *Pages) contactPhotoFromFace(username, contactUID string, galleryDB *sql.DB, personID int64) error {
	person, err := GetPerson(galleryDB, personID)
	if err != nil {
		return fmt.Errorf("person: %w", err)
	}
	if person.BestFaceID == nil {
		return nil // no face to use
	}
	face, err := GetFaceWithMedia(galleryDB, *person.BestFaceID)
	if err != nil {
		return fmt.Errorf("face %d: %w", *person.BestFaceID, err)
	}
	m, err := GetMedia(galleryDB, face.MediaID)
	if err != nil {
		return fmt.Errorf("media %d: %w", face.MediaID, err)
	}
	thumbFile, err := h.plugin.PreviewPath(username, m, 1024)
	if err != nil {
		return fmt.Errorf("thumbnail: %w", err)
	}
	defer thumbFile.Close()
	img, _, err := image.Decode(thumbFile)
	if err != nil {
		return fmt.Errorf("decode thumbnail: %w", err)
	}
	return h.plugin.contacts.SetPhoto(username, contactUID, cropFace(img, &face.Face))
}

// cropFace crops a face region from an image with padding.
func cropFace(img image.Image, face *Face) image.Image {
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())
	fx := face.X * imgW
	fy := face.Y * imgH
	fw := face.W * imgW
	fh := face.H * imgH

	padW := fw * 0.2
	padH := fh * 0.2
	x0 := int(max(fx-padW, 0))
	y0 := int(max(fy-padH, 0))
	x1 := int(min(fx+fw+padW, imgW))
	y1 := int(min(fy+fh+padH, imgH))

	return cropImage(img, image.Rect(bounds.Min.X+x0, bounds.Min.Y+y0, bounds.Min.X+x1, bounds.Min.Y+y1))
}

// HidePerson hides a person from the people view.
func (h *Pages) HidePerson(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := HidePersonRow(db, id); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, "/apps/gallery/people", http.StatusSeeOther)
}

// RemoveFaceFromPerson moves a face to a new person so it can be merged elsewhere.
func (h *Pages) RemoveFaceFromPerson(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", personID), http.StatusSeeOther)
}

// AddMediaTagAction adds a tag to a media item.
func (h *Pages) AddMediaTagAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	if err := AddMediaTag(db, mediaID, tagID, 1.0); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// RemoveMediaTagAction removes a tag from a media item.
func (h *Pages) RemoveMediaTagAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// DeleteFaceAction deletes a face detection from a media item.
func (h *Pages) DeleteFaceAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/photo/%d", mediaID), http.StatusSeeOther)
}

// CreatePersonFromFaceAction creates a new person from a face and redirects to the new person.
func (h *Pages) CreatePersonFromFaceAction(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	faceID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	personID, err := CreatePersonFromFace(db, faceID)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/person/%d", personID), http.StatusSeeOther)
}

// SearchTagsAPI returns tag search results as picker options.
func (h *Pages) SearchTagsAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	q := r.URL.Query().Get("q")
	tags, err := SearchTags(db, q, 10)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	items := make([]ui.PickerItem, len(tags))
	for i, t := range tags {
		items[i] = ui.PickerItem{ID: t.Name, Name: t.Name}
	}
	ui.Render(w, r, ui.PickerResults(items))
}

// SearchPeopleAPI returns people search results as picker options.
// Results include both gallery people and contacts (if the contacts plugin is installed).
func (h *Pages) SearchPeopleAPI(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	q := r.URL.Query().Get("name")
	if q == "" {
		q = r.URL.Query().Get("q")
	}
	excludeID, _ := strconv.ParseInt(r.URL.Query().Get("exclude"), 10, 64)
	people, err := SearchPeople(db, q, excludeID, 10)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
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

	// Include matching contacts if the contacts plugin is available.
	username := plugin.Username(r)
	if h.plugin.contacts != nil {
		var cards []contacts.CardWithBook
		cs, err := h.plugin.contacts.Store(username)
		if err == nil {
			cards, err = cs.Search(nil, q, 5)
		}
		if err != nil {
			h.plugin.log.Error("search contacts", "user", username, "err", err)
		}
		for _, card := range cards {
			// Skip contacts already linked to a gallery person.
			if _, err := PersonByContactUID(db, card.UID); err == nil {
				continue
			} else if !errors.Is(err, sql.ErrNoRows) {
				h.plugin.log.Error("person for contact", "uid", card.UID, "err", err)
				continue
			}
			item := ui.PickerItem{
				ID:   "contact:" + card.UID,
				Name: card.FN,
				Sub:  "Contact",
			}
			if card.HasPhoto() {
				item.AvatarURL = fmt.Sprintf("/apps/contacts/photos/%d", card.ID)
			}
			items = append(items, item)
		}
	}

	ui.Render(w, r, ui.PickerResults(items))
}

// TriggerScan manually triggers a media scan via the scan worker subprocess.
func (h *Pages) TriggerScan(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	go h.plugin.requestScan(username)
	flashRedirect(w, r, "success", "Scanning for new photos. They appear in the timeline as they are found.", "/apps/gallery/settings")
}

// SaveScanFolders saves the per-user scan folders setting.
func (h *Pages) SaveScanFolders(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	folders := r.FormValue("folders")
	// Validate at least one non-empty line.
	hasLine := false
	for line := range strings.SplitSeq(folders, "\n") {
		if strings.TrimSpace(line) != "" {
			hasLine = true
			break
		}
	}
	if !hasLine {
		flashRedirect(w, r, "error", "Enter at least one folder pattern.", "/apps/gallery/settings")
		return
	}
	if err := SettingScanFolders.Set(db, folders); err != nil {
		flashRedirect(w, r, "error", "The folders could not be saved: "+err.Error(), "/apps/gallery/settings")
		return
	}
	flashRedirect(w, r, "success", "Saved the folders to scan.", "/apps/gallery/settings")
}

// ScanFoldersPreview returns an HTMX partial showing which folders match the given patterns.
func (h *Pages) ScanFoldersPreview(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	filesDB, err := h.plugin.ctx.PluginDB(username, plugin.OfPlugin("files"))
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	// Parse patterns from the textarea (sent as a query parameter).
	raw := r.FormValue("folders")
	var patterns []string
	for line := range strings.SplitSeq(raw, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			patterns = append(patterns, line)
		}
	}
	if len(patterns) == 0 {
		patterns = []string{"Photos/"}
	}

	// Query all media file paths from files DB.
	rows, err := filesDB.Query(`SELECT path FROM files
		WHERE is_dir = 0 AND (mime_type LIKE 'image/%' OR mime_type LIKE 'video/%')`)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	defer rows.Close()

	folderSet := map[string]int{}
	total := 0
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			h.serverError(w, r, err)
			return
		}
		if matchScanFolders(p, patterns) {
			total++
			// Extract top-level folder.
			dir := TrimScanPath(p)
			if idx := strings.Index(dir, "/"); idx >= 0 {
				dir = dir[:idx+1]
			}
			folderSet[dir]++
		}
	}
	if err := rows.Err(); err != nil {
		h.serverError(w, r, err)
		return
	}

	var folders []string
	for f, count := range folderSet {
		folders = append(folders, fmt.Sprintf("%s (%s)", f, plural(count, "file", "files")))
	}
	sort.Strings(folders)

	ui.Render(w, r, scanFoldersPreview(folders, total))
}

// SaveRecognitionSettings saves recognition backend settings (legacy per-user, redirects to system settings).
func (h *Pages) SaveRecognitionSettings(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// SystemSettings renders the system-level gallery settings page (admin only).
func (h *Pages) SystemSettings(w http.ResponseWriter, r *http.Request) {
	sysDB, err := h.plugin.SystemDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	stats := h.plugin.AggregateRecognitionStats()
	scanStatus := h.plugin.GetScanStatus()
	plugin.SetTitle(w, "Gallery Settings")
	ui.Render(w, r, systemSettingsPage(sysDB, stats, scanStatus, plugin.CSRFToken(r)))
}

// ScanStatusPartial returns the scan status card as an HTMX partial.
func (h *Pages) ScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	stats := h.plugin.AggregateRecognitionStats()
	scan := h.plugin.GetScanStatus()
	ui.Render(w, r, scanStatusCard(stats, scan, plugin.CSRFToken(r)))
}

// UserScanStatusPartial returns a small scan-running banner for the gallery sidebar.
func (h *Pages) UserScanStatusPartial(w http.ResponseWriter, r *http.Request) {
	scan := h.plugin.GetScanStatus()
	ui.Render(w, r, userScanBanner(scan))
}

// TriggerSystemScan triggers a full scan of all users from the admin page.
func (h *Pages) TriggerSystemScan(w http.ResponseWriter, r *http.Request) {
	go h.plugin.requestScan("*")
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// PurgeRecognitions purges all recognition data and resets state for all users.
func (h *Pages) PurgeRecognitions(w http.ResponseWriter, r *http.Request) {
	if err := h.plugin.PurgeAllRecognitions(); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// SaveSystemSettings saves system-level gallery settings.
func (h *Pages) SaveSystemSettings(w http.ResponseWriter, r *http.Request) {
	db, err := h.plugin.SystemDB()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	// Each settings card is a separate form with a _section hidden field.
	// Only save settings for the submitted section to avoid overwriting
	// checkbox values from the other form (unchecked checkboxes are absent).
	var kv [][2]string
	set := func(key string) { kv = append(kv, [2]string{key, r.FormValue(key)}) }
	setIfGiven := func(key string) {
		if r.FormValue(key) != "" {
			set(key)
		}
	}
	switch r.FormValue("_section") {
	case "tags":
		set("tag_backend")
		setIfGiven("tag_model")
		setIfGiven("tag_min_confidence")
	case "faces":
		set("face_backend")
		setIfGiven("face_min_confidence")
		setIfGiven("cluster_threshold")
	case "duplicates":
		set("duplicate_detection_enabled")
		setIfGiven("duplicate_algorithm")
		setIfGiven("duplicate_distance")
	case "ocr":
		set("ocr_enabled")
	case "processing":
		set("preview_generation_enabled")
		set("quality_scoring_enabled")
	case "backend":
		set("onnx_library_path")
		set("onnx_models_dir")
		set("recognition_url")
		set("recognition_api_key")
		set("recognition_model")
	}
	if err := SetSettings(db, kv); err != nil {
		h.plugin.log.Error("save gallery settings", "err", err)
		flashRedirect(w, r, "error", "The settings could not be saved: "+err.Error(), "/admin/plugins/gallery")
		return
	}

	http.Redirect(w, r, "/admin/plugins/gallery", http.StatusSeeOther)
}

// --- Duplicate handlers ---

// Starred renders the starred photos page.
func (h *Pages) Starred(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	username := plugin.Username(r)
	var media []Media
	if h.plugin.files != nil {
		filesDB, err := h.plugin.ctx.PluginDB(username, plugin.OfPlugin("files"))
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		fileIDs, err := StarredFileIDs(filesDB)
		if err != nil {
			h.serverError(w, r, err)
			return
		}
		if media, err = StarredMedia(db, fileIDs); err != nil {
			h.serverError(w, r, err)
			return
		}
	}
	plugin.SetTitle(w, "Starred")
	ui.Render(w, r, starredPage(media))
}

// TogglePhotoStar toggles the favorite state of a photo in the files plugin.
func (h *Pages) TogglePhotoStar(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	m, err := GetMedia(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	if h.plugin.files == nil {
		http.Error(w, "files plugin unavailable", http.StatusInternalServerError)
		return
	}
	username := plugin.Username(r)
	filesDB, err := h.plugin.ctx.PluginDB(username, plugin.OfPlugin("files"))
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	// Read current state and toggle.
	var fav int
	filesDB.QueryRow(`SELECT favorite FROM files WHERE id = ?`, m.FileID).Scan(&fav)
	newFav := fav != 1
	h.plugin.files.SetFavorite(username, m.FilePath, newFav)

	// Return updated star button.
	ui.Render(w, r, ui.Star(newFav, fmt.Sprintf("/apps/gallery/media/%d/star", id)))
}

// Duplicates renders the duplicate groups list.
func (h *Pages) Duplicates(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	groups, err := ListDuplicateGroups(db)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Duplicates")
	ui.Render(w, r, duplicatesPage(r.URL.Path, groups))
}

// DuplicateGroupView renders a single duplicate group.
func (h *Pages) DuplicateGroupView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, err := DuplicateGroupMembers(db, id)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	plugin.SetTitle(w, "Duplicate Group")
	ui.Render(w, r, duplicateGroupViewPage(r.URL.Path, id, members, plugin.CSRFToken(r)))
}

// SetDuplicateKeep sets the keep item in a duplicate group.
func (h *Pages) SetDuplicateKeep(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/apps/gallery/duplicates/%d", groupID), http.StatusSeeOther)
}

// UnmarkDuplicate removes a member from a duplicate group.
func (h *Pages) UnmarkDuplicate(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	h.redirectAfterDuplicateChange(w, r, db, groupID, nil)
}

// deleteDuplicateFiles moves each member's file to the trash and drops it
// from the gallery (which drops it from its duplicate group). A file that
// cannot be deleted stays in the gallery and is named in failed; err is a
// database failure.
func (h *Pages) deleteDuplicateFiles(db *sql.DB, username string, members []DuplicateMember) (failed []string, err error) {
	for _, m := range members {
		if err := h.plugin.files.Delete(username, m.Media.FilePath); err != nil {
			h.plugin.log.Warn("delete duplicate", "path", m.Media.FilePath, "err", err)
			failed = append(failed, path.Base(m.Media.FilePath))
			continue
		}
		if err := DeleteMediaByFileID(db, m.Media.FileID); err != nil {
			return failed, fmt.Errorf("forget %s: %w", m.Media.FilePath, err)
		}
	}
	return failed, nil
}

// pruneDuplicateGroup deletes a group with fewer than two members left, and
// reports whether it did.
func pruneDuplicateGroup(db *sql.DB, groupID int64) (bool, error) {
	count, err := DuplicateGroupMemberCount(db, groupID)
	if err != nil {
		return false, fmt.Errorf("count group %d: %w", groupID, err)
	}
	if count >= 2 {
		return false, nil
	}
	if err := DeleteDuplicateGroup(db, groupID); err != nil {
		return false, fmt.Errorf("delete group %d: %w", groupID, err)
	}
	return true, nil
}

// redirectAfterDuplicateChange prunes the group and goes back to it, or to
// the list when it is gone, saying which files could not be deleted.
func (h *Pages) redirectAfterDuplicateChange(w http.ResponseWriter, r *http.Request, db *sql.DB, groupID int64, failed []string) {
	gone, err := pruneDuplicateGroup(db, groupID)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	to := fmt.Sprintf("/apps/gallery/duplicates/%d", groupID)
	if gone {
		to = "/apps/gallery/duplicates"
	}
	if len(failed) > 0 {
		flashRedirect(w, r, "error", "Could not delete "+strings.Join(failed, ", ")+".", to)
		return
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// DeleteNonKeepDuplicates deletes non-keep files in a duplicate group.
func (h *Pages) DeleteNonKeepDuplicates(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	failed, err := h.deleteDuplicateFiles(db, username, nonKeep(members))
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	h.redirectAfterDuplicateChange(w, r, db, groupID, failed)
}

// nonKeep is the members not marked to keep.
func nonKeep(members []DuplicateMember) []DuplicateMember {
	var out []DuplicateMember
	for _, m := range members {
		if !m.IsKeep {
			out = append(out, m)
		}
	}
	return out
}

// DeleteAllNonKeep deletes non-keep files across all duplicate groups.
func (h *Pages) DeleteAllNonKeep(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	members, err := NonKeepDuplicates(db)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	failed, err := h.deleteDuplicateFiles(db, plugin.Username(r), members)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if err := PruneDuplicateGroups(db); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if len(failed) > 0 {
		flashRedirect(w, r, "error", "Could not delete "+strings.Join(failed, ", ")+".", "/apps/gallery/duplicates")
		return
	}
	http.Redirect(w, r, "/apps/gallery/duplicates", http.StatusSeeOther)
}

// DeleteDuplicateMember deletes a single member from a duplicate group.
func (h *Pages) DeleteDuplicateMember(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
	username := plugin.Username(r)
	m, err := GetMedia(db, mediaID)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	// Deleting the media drops its membership too (ON DELETE CASCADE).
	failed, err := h.deleteDuplicateFiles(db, username, []DuplicateMember{{Media: *m}})
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	h.redirectAfterDuplicateChange(w, r, db, groupID, failed)
}

// DeleteAllDuplicates deletes all files in a duplicate group, including the keep.
func (h *Pages) DeleteAllDuplicates(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
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
		httperr.Internal(w, r, err)
		return
	}
	failed, err := h.deleteDuplicateFiles(db, username, members)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	h.redirectAfterDuplicateChange(w, r, db, groupID, failed)
}

// DismissDuplicateGroup removes a group from the duplicates list without deleting files.
func (h *Pages) DismissDuplicateGroup(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	groupID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := DismissDuplicateGroup(db, groupID); err != nil {
		httperr.Internal(w, r, err)
		return
	}
	http.Redirect(w, r, "/apps/gallery/duplicates", http.StatusSeeOther)
}

// RenamePreview shows what renaming every photo with ?pattern= would do. It
// is a GET page, so a refresh does not resubmit anything and the pattern
// can be edited and previewed again in place.
func (h *Pages) RenamePreview(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		http.Redirect(w, r, "/apps/gallery/settings", http.StatusSeeOther)
		return
	}
	previews, err := PreviewRename(db, pattern)
	var patternErr string
	if err != nil {
		patternErr = err.Error()
	}
	plugin.SetTitle(w, "Rename Preview")
	ui.Render(w, r, renamePreviewPage(pattern, previews, patternErr, plugin.CSRFToken(r)))
}

// RenameApply executes the rename operation.
func (h *Pages) RenameApply(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	pattern := r.FormValue("pattern")
	previews, err := PreviewRename(db, pattern)
	if err != nil {
		flashRedirect(w, r, "error", "Nothing was renamed: "+err.Error(), "/apps/gallery/rename/preview?pattern="+url.QueryEscape(pattern))
		return
	}
	username := plugin.Username(r)
	if err := h.plugin.ApplyRename(username, previews); err != nil {
		flashRedirect(w, r, "error", "The rename stopped part way: "+err.Error()+". Undo Last Rename puts back what was renamed.", "/apps/gallery/settings")
		return
	}
	flashRedirect(w, r, "success", "Renamed "+plural(len(previews), "file", "files")+".", "/apps/gallery/settings")
}

// RenameUndo reverses the most recent rename batch.
func (h *Pages) RenameUndo(w http.ResponseWriter, r *http.Request) {
	username := plugin.Username(r)
	if err := h.plugin.UndoLastRename(username); err != nil {
		flashRedirect(w, r, "error", "The last rename could not be undone: "+err.Error(), "/apps/gallery/settings")
		return
	}
	flashRedirect(w, r, "success", "Undid the last rename.", "/apps/gallery/settings")
}

// plural is "1 photo" or "3 photos".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
