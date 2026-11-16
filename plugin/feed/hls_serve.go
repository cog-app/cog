package feed

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	opts, err := parseHLSOptions(q)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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
	key, err := store.ExistingHLSKey()
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	sig := q.Get("sig")
	if key == nil || !validHLSSignature(key, username, itemID, opts, sig) {
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
	query := opts.query(sig)
	// Captions are masked whenever the clean rendition plays. Without a
	// profanity plugin there is nothing to mask with, so they are withheld.
	captions := d.CaptionsPath != ""
	if captions && d.CleanTrackPath != "" && p.Profanity() == nil {
		captions = false
		if file == "master.m3u8" {
			slog.Warn("feed hls: captions not masked; profanity plugin unavailable", "item", itemID)
		}
	}
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
		original := hls.Rendition{Name: "Original", URI: "audio.m3u8" + query, Default: true, Track: &audio}
		pres := hls.Presentation{Audio: []hls.Rendition{original}, Video: video, VideoURI: "video.m3u8" + query}
		if d.CleanTrackPath != "" {
			clean, _, err := space.Track(d.CleanTrackPath)
			if err != nil {
				httperr.Internal(w, r, err)
				return
			}
			original.Default = false
			// Original is listed first, so a player that honours DEFAULT=YES
			// is told apart from one that takes the first rendition.
			pres.Audio = []hls.Rendition{original, {Name: "Clean", URI: "clean.m3u8" + query, Default: true, Track: &clean}}
		}
		if captions {
			if _, err := readCaptions(space, d.CaptionsPath); err != nil {
				slog.Warn("feed hls: captions", "item", itemID, "err", err)
			} else {
				pres.Subtitles = []hls.Rendition{{Name: "Captions", URI: "subs.m3u8" + query}}
			}
		}
		writePlaylist(w, r, hls.Master(pres))
	case "subs.m3u8":
		if !captions {
			http.NotFound(w, r)
			return
		}
		audio, _, err := space.Track(d.LocalPath)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		writePlaylist(w, r, hls.Playlist(hls.SubtitleEntries(audio, func(n int) string {
			return "subs-" + strconv.Itoa(n) + ".vtt" + query
		})))
	case "audio.m3u8", "video.m3u8", "clean.m3u8":
		stored, media := d.LocalPath, "audio.m4a"
		switch file {
		case "video.m3u8":
			stored, media = d.VideoPath, "video.mp4"
		case "clean.m3u8":
			stored, media = d.CleanTrackPath, "clean.m4a"
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
	case "audio.m4a":
		serveTrackFile(w, r, space, d.LocalPath, "audio/mp4")
	case "video.mp4":
		serveTrackFile(w, r, space, d.VideoPath, "video/mp4")
	case "clean.m4a":
		serveTrackFile(w, r, space, d.CleanTrackPath, "audio/mp4")
	default:
		n, ok := subsSegment(file)
		if !ok || !captions {
			http.NotFound(w, r)
			return
		}
		audio, _, err := space.Track(d.LocalPath)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		start, end, ok := hls.SegmentCaptionBounds(audio, n)
		if !ok {
			http.NotFound(w, r)
			return
		}
		cues, err := readCaptions(space, d.CaptionsPath)
		if err != nil {
			httperr.Internal(w, r, err)
			return
		}
		if d.CleanTrackPath != "" {
			// The clean rendition is the one that plays, so its captions say
			// nothing the audio does not; captions is false without the plugin.
			prof := p.Profanity()
			for i := range cues {
				cues[i].Text = prof.FilterText(username, cues[i].Text)
			}
		}
		// MPEGTS 0: hls.js rebases the timeline so the first audio sample plays
		// at 0, and this puts caption time 0 there (TestBrowser/HLSCaptionsOffset).
		writeVTT(w, r, hls.VTTSegment(cues, start, end, 0))
	}
}

// serveTrackFile serves a stored track file with byte ranges; 404 when the
// item has none.
func serveTrackFile(w http.ResponseWriter, r *http.Request, space Space, stored, mime string) {
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
}

// subsSegment is the segment number of a file named subs-<n>.vtt.
func subsSegment(file string) (int, bool) {
	num, ok := strings.CutPrefix(file, "subs-")
	if !ok {
		return 0, false
	}
	num, ok = strings.CutSuffix(num, ".vtt")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	return n, err == nil && n >= 0
}

// readCaptions parses the item's stored captions file.
func readCaptions(space Space, stored string) ([]hls.Cue, error) {
	f, err := space.Own.Open(Name(stored))
	if err != nil {
		return nil, fmt.Errorf("open captions: %w", err)
	}
	cues, err := hls.ParseVTT(f)
	if cerr := f.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close captions: %w", cerr))
	}
	if err != nil {
		return nil, fmt.Errorf("read captions: %w", err)
	}
	return cues, nil
}

func writePlaylist(w http.ResponseWriter, r *http.Request, body string) {
	writeText(w, r, "application/vnd.apple.mpegurl", body)
}

func writeVTT(w http.ResponseWriter, r *http.Request, body string) {
	writeText(w, r, "text/vtt; charset=utf-8", body)
}

func writeText(w http.ResponseWriter, r *http.Request, contentType, body string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write([]byte(body)); err != nil {
		slog.Debug("feed hls: write playlist", "err", err)
	}
}
