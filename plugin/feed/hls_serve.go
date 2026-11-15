package feed

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// hlsRoute is where an item's presentation is served. It has no session:
// the signature in its query is the grant (hls_links.go).
const hlsRoute = "GET /apps/feed/hls/{user}/{item}/{file}"

// ServeHLS serves the files of a downloaded item's HLS presentation: the
// master and media playlists, written from the tracks' indexes on each
// request, and the track files, with byte ranges.
func (p *Plugin) ServeHLS(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for k := range q {
		if k != "sig" {
			http.Error(w, "unknown parameter "+strconv.Quote(k), http.StatusBadRequest)
			return
		}
	}
	username, file := r.PathValue("user"), r.PathValue("file")
	itemID, err := strconv.ParseInt(r.PathValue("item"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lookup := p.ctx.UserLookup()
	if lookup == nil {
		httperr.Internal(w, r, errors.New("feed hls: no user lookup"))
		return
	}
	if _, err := lookup.GetByUsername(username); err != nil {
		if errors.Is(err, user.ErrNotFound) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		httperr.Internal(w, r, fmt.Errorf("feed hls: look up user: %w", err))
		return
	}
	db, err := p.DB(username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	store := NewStore(db)
	key, err := store.HLSKey()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sig := q.Get("sig")
	if !validHLSSignature(key, username, itemID, sig) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	d, err := store.FindDownload(itemID)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if d.PackageState != "done" {
		http.NotFound(w, r)
		return
	}
	space, err := p.Space(username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	query := "?sig=" + sig
	switch file {
	case "master.m3u8":
		audio, _, err := space.Track(d.LocalPath)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		var video *hls.Track
		if d.VideoPath != "" {
			v, _, err := space.Track(d.VideoPath)
			if err != nil {
				httperr.Internal(w, r, err)
				return
			}
			video = &v
		}
		writePlaylist(w, r, hls.MasterPlaylist(audio, video, "audio.m3u8"+query, "video.m3u8"+query))
	case "audio.m3u8", "video.m3u8":
		stored, media := d.LocalPath, "audio.m4a"
		if file == "video.m3u8" {
			stored, media = d.VideoPath, "video.mp4"
		}
		if stored == "" {
			http.NotFound(w, r)
			return
		}
		t, _, err := space.Track(stored)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		writePlaylist(w, r, hls.MediaPlaylist(t, media+query))
	case "audio.m4a", "video.mp4":
		stored, mime := d.LocalPath, "audio/mp4"
		if file == "video.mp4" {
			stored, mime = d.VideoPath, "video/mp4"
		}
		if stored == "" {
			http.NotFound(w, r)
			return
		}
		f, err := space.Own.Open(Name(stored))
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		defer func() {
			if err := f.Close(); err != nil {
				slog.Warn("feed hls: close track", "path", stored, "err", err)
			}
		}()
		st, err := f.Stat()
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		w.Header().Set("Content-Type", mime)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, "", st.ModTime(), f)
	default:
		http.NotFound(w, r)
	}
}

func writePlaylist(w http.ResponseWriter, r *http.Request, body string) {
	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write([]byte(body)); err != nil {
		slog.Debug("feed hls: write playlist", "err", err)
	}
}
