package gallery

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

const albumResourceType = "album"

// SharedAlbum is an album someone else shared with the user, read from the
// owner's gallery.
type SharedAlbum struct {
	Album
	Owner string
}

// sharedAlbums returns the albums shared with userID, newest first.
func (p *Plugin) sharedAlbums(userID string) ([]SharedAlbum, error) {
	shares, users := p.ctx.ShareStore(), p.ctx.UserLookup()
	if shares == nil || users == nil {
		return nil, nil
	}
	byOwner := map[string][]int64{}
	for rec, err := range shares.ListBySubject(userID) {
		if err != nil {
			return nil, fmt.Errorf("shares with %s: %w", userID, err)
		}
		if rec.ResourceType != PluginID+":"+albumResourceType || rec.OwnerUserID == userID {
			continue
		}
		id, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("share %s: album id %q: %w", rec.ID, rec.ResourceID, err)
		}
		byOwner[rec.OwnerUserID] = append(byOwner[rec.OwnerUserID], id)
	}
	var out []SharedAlbum
	for ownerID, ids := range byOwner {
		owner, err := users.GetByID(ownerID)
		if err != nil {
			return nil, fmt.Errorf("owner %s: %w", ownerID, err)
		}
		db, err := p.DB(owner.Username)
		if err != nil {
			return nil, err
		}
		albums, err := albumsByID(db, ids)
		if err != nil {
			return nil, fmt.Errorf("albums of %s: %w", owner.Username, err)
		}
		for _, a := range albums {
			out = append(out, SharedAlbum{Album: a, Owner: owner.Username})
		}
	}
	slices.SortFunc(out, func(a, b SharedAlbum) int { return cmp.Compare(b.UpdatedAt, a.UpdatedAt) })
	return out, nil
}

func albumsByID(db *sql.DB, ids []int64) ([]Album, error) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := db.Query(`SELECT a.id, a.name, a.description, a.album_type, a.auto_key,
		`+albumCoverCol+`, a.sort_order, a.created_at, a.updated_at,
		(SELECT COUNT(*) FROM album_media am WHERE am.album_id = a.id) AS media_count
		FROM albums a WHERE a.id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAlbumRows(rows)
}

// albumCollection is an album as a folder of its owner's files.
func (p *Plugin) albumCollection(owner, resourceID string) (string, []string, error) {
	id, err := strconv.ParseInt(resourceID, 10, 64)
	if err != nil {
		return "", nil, cogplugin.ErrResourceGone
	}
	db, err := p.DB(owner)
	if err != nil {
		return "", nil, err
	}
	var name string
	if err := db.QueryRow(`SELECT name FROM albums WHERE id = ?`, id).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return "", nil, cogplugin.ErrResourceGone
	} else if err != nil {
		return "", nil, err
	}
	rows, err := db.Query(`SELECT m.file_path FROM media m JOIN album_media am ON am.media_id = m.id
		WHERE am.album_id = ?`, id)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	var paths []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return "", nil, err
		}
		paths = append(paths, fp)
	}
	return name, paths, rows.Err()
}

// errNotShared hides whether an album exists from someone it was not shared
// with.
var errNotShared = errors.New("album not shared")

// sharedAlbumDB returns the owner's gallery database when the album was
// shared with userID by that owner. Only a share created by the owner counts:
// an album ID means nothing outside its owner's gallery.
func (p *Plugin) sharedAlbumDB(userID, owner string, albumID int64) (*sql.DB, error) {
	shares, users := p.ctx.ShareStore(), p.ctx.UserLookup()
	if shares == nil || users == nil {
		return nil, errNotShared
	}
	o, err := users.GetByUsername(owner)
	if err != nil || o == nil {
		return nil, errNotShared
	}
	want := strconv.FormatInt(albumID, 10)
	for rec, err := range shares.ListBySubject(userID) {
		if err != nil {
			return nil, fmt.Errorf("shares with %s: %w", userID, err)
		}
		if rec.ResourceType == PluginID+":"+albumResourceType && rec.ResourceID == want &&
			rec.OwnerUserID == o.ID && rec.Permissions&plugin.PermRead != 0 {
			return p.DB(owner)
		}
	}
	return nil, errNotShared
}

func sharedAlbumURL(owner string, id int64) string {
	return fmt.Sprintf("/apps/gallery/shared/%s/album/%d", owner, id)
}

func sharedThumbURL(owner string, albumID, mediaID int64, size int) string {
	return fmt.Sprintf("/apps/gallery/api/shared/%s/album/%d/media/%d/thumbnail?size=%d", owner, albumID, mediaID, size)
}

// sharedAlbumRequest resolves the owner and album in the path, answering
// the request itself when it cannot.
func (h *Pages) sharedAlbumRequest(w http.ResponseWriter, r *http.Request) (*sql.DB, string, int64, bool) {
	owner := r.PathValue("owner")
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return nil, "", 0, false
	}
	u := plugin.UserFromContext(r.Context())
	if u == nil {
		http.NotFound(w, r)
		return nil, "", 0, false
	}
	db, err := h.plugin.sharedAlbumDB(u.ID, owner, id)
	if errors.Is(err, errNotShared) {
		http.NotFound(w, r)
		return nil, "", 0, false
	}
	if err != nil {
		h.serverError(w, r, err)
		return nil, "", 0, false
	}
	return db, owner, id, true
}

// SharedAlbumView renders an album someone shared with the user.
func (h *Pages) SharedAlbumView(w http.ResponseWriter, r *http.Request) {
	db, owner, id, ok := h.sharedAlbumRequest(w, r)
	if !ok {
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
	plugin.SetTitle(w, album.Name)
	ui.Render(w, r, sharedAlbumPage("/apps/gallery/albums", SharedAlbum{Album: *album, Owner: owner}, media))
}

// SharedAlbumThumbnail serves a preview of a photo in a shared album, made
// from the owner's file.
func (h *Pages) SharedAlbumThumbnail(w http.ResponseWriter, r *http.Request) {
	db, owner, id, ok := h.sharedAlbumRequest(w, r)
	if !ok {
		return
	}
	mediaID, err := strconv.ParseInt(r.PathValue("media_id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	m, err := scanMedia(db.QueryRow(`SELECT `+mediaCols+` FROM media m
		JOIN album_media am ON am.media_id = m.id
		WHERE am.album_id = ? AND m.id = ?`, id, mediaID))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.serverError(w, r, err)
		return
	}
	size := 256
	if v, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil {
		size = v
	}
	f, err := h.plugin.PreviewPath(owner, m, size)
	if err != nil {
		h.plugin.log.Warn("shared thumbnail", "owner", owner, "media", m.ID, "err", err)
		http.Error(w, "thumbnail unavailable", http.StatusNotFound)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeContent(w, r, "thumbnail.jpg", time.Time{}, f)
}
