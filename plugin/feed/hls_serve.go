package feed

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// hlsRoute is where an item's presentation is served. It has no session:
// the signature in its query is the grant (hls_links.go).
const hlsRoute = "GET /apps/feed/hls/{user}/{item}/{file}"

// ServeHLS serves the files of a downloaded item's HLS presentation: the
// master and media playlists, written from the tracks' indexes on each
// request, and the track files, with byte ranges. With skip=silence, the
// master picks the presentation and names it by edit list, and the files
// with that edit= follow its plan, edge files included (hls_skip.go).
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
	// Captions are masked whenever the clean rendition plays. Without a
	// profanity plugin there is nothing to mask with, so they are withheld,
	// with and without skip.
	captions := d.CaptionsPath != ""
	if captions && d.CleanTrackPath != "" && p.Profanity() == nil {
		captions = false
		if file == "master.m3u8" {
			slog.Warn("feed hls: captions not masked; profanity plugin unavailable", "item", itemID)
		}
	}
	h := hlsRequest{p: p, w: w, r: r, store: store, space: space, d: d, username: username, sig: sig, opts: opts, captions: captions}
	switch {
	case file == "master.m3u8":
		h.master()
	case opts.Edit != "":
		h.skipFile(file)
	default:
		h.file(file)
	}
}

// hlsRequest is one request for a file of an item's presentation, once
// its signature is checked.
type hlsRequest struct {
	p        *Plugin
	w        http.ResponseWriter
	r        *http.Request
	store    *Store
	space    Space
	d        Download
	username string
	sig      string
	opts     hlsOptions
	// captions is whether the presentation has captions: the item has them,
	// and they can be masked when the clean rendition plays.
	captions bool
}

// itemTracks are the tracks of an item's renditions: clean and video are
// nil when it has none.
type itemTracks struct {
	original     hls.Track
	clean, video *hls.Track
}

// loadTracks reads the fragment index of every track d's presentation lists.
func loadTracks(space Space, d Download) (itemTracks, error) {
	var t itemTracks
	var err error
	if t.original, _, err = space.Track(d.LocalPath); err != nil {
		return itemTracks{}, err
	}
	if d.CleanTrackPath != "" {
		c, _, err := space.Track(d.CleanTrackPath)
		if err != nil {
			return itemTracks{}, err
		}
		t.clean = &c
	}
	if d.VideoPath != "" {
		v, _, err := space.Track(d.VideoPath)
		if err != nil {
			return itemTracks{}, err
		}
		t.video = &v
	}
	return t, nil
}

// presentation is what the master lists, before any skip: Original first,
// then Clean as the default when there is one, the video, and the captions
// when subs. Each file is named with query.
func presentation(t itemTracks, query string, subs bool) hls.Presentation {
	original := hls.Rendition{Name: "Original", URI: "audio.m3u8" + query, Default: true, Track: &t.original}
	pres := hls.Presentation{Audio: []hls.Rendition{original}, Video: t.video, VideoURI: "video.m3u8" + query}
	if t.clean != nil {
		original.Default = false
		// Original is listed first, so a player that honours DEFAULT=YES
		// is told apart from one that takes the first rendition.
		pres.Audio = []hls.Rendition{original, {Name: "Clean", URI: "clean.m3u8" + query, Default: true, Track: t.clean}}
	}
	if subs {
		pres.Subtitles = []hls.Rendition{{Name: "Captions", URI: "subs.m3u8" + query}}
	}
	return pres
}

// master writes the master playlist. With skip, it picks the presentation
// (hls_skip.go) and is the only request that writes: what the players want
// and the edit lists it served.
func (h hlsRequest) master() {
	tracks, err := loadTracks(h.space, h.d)
	if err != nil {
		hlsError(h.w, h.r, err)
		return
	}
	subs := h.captions
	if subs {
		if _, err := readCaptions(h.space, h.d.CaptionsPath); err != nil {
			slog.Warn("feed hls: captions", "item", h.d.ItemID, "err", err)
			subs = false
		}
	}
	if !h.opts.Skip {
		writePlaylist(h.w, h.r, hls.Master(presentation(tracks, h.opts.query(h.sig), subs)))
		return
	}
	sel, err := h.masterSkip(tracks)
	if err != nil {
		hlsError(h.w, h.r, err)
		return
	}
	pres := presentation(tracks, hlsOptions{Skip: true, Edit: sel.edit}.query(h.sig), subs)
	if sel.coarse {
		// Coarse cuts follow one segment grid, so only the default audio
		// rendition is listed (it is the last).
		def := pres.Audio[len(pres.Audio)-1]
		def.Default = true
		pres.Audio = []hls.Rendition{def}
	}
	if sel.status != "" {
		pres.SessionData = append(pres.SessionData, [2]string{"us.duckfam.cog.skip", sel.status})
	}
	if sel.edit != "" {
		pres.SessionData = append(pres.SessionData, [2]string{"us.duckfam.cog.edits", sel.edit})
	}
	writePlaylist(h.w, h.r, hls.Master(pres))
}

// file serves a file of the unskipped presentation.
func (h hlsRequest) file(file string) {
	w, r, d, space := h.w, h.r, h.d, h.space
	query := h.opts.query(h.sig)
	switch file {
	case "subs.m3u8":
		if !h.captions {
			http.NotFound(w, r)
			return
		}
		audio, _, err := space.Track(d.LocalPath)
		if err != nil {
			hlsError(w, r, err)
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
			hlsError(w, r, err)
			return
		}
		writePlaylist(w, r, hls.MediaPlaylist(t, media+query))
	case "audio.m4a", "video.mp4", "clean.m4a":
		h.trackFile(file)
	default:
		n, ok := subsSegment(file)
		if !ok || !h.captions {
			http.NotFound(w, r)
			return
		}
		audio, _, err := space.Track(d.LocalPath)
		if err != nil {
			hlsError(w, r, err)
			return
		}
		start, end, ok := hls.SegmentCaptionBounds(audio, n)
		if !ok {
			http.NotFound(w, r)
			return
		}
		cues, err := h.captionCues()
		if err != nil {
			hlsError(w, r, err)
			return
		}
		// Caption time 0 is the Original's first sample, at its media time
		// (a clean track starts there too): the spec's mapping, which hls.js
		// follows against the first tfdt (TestBrowser/HLSCaptionsOffset).
		writeVTT(w, r, hls.VTTSegment(cues, start, end, hls.TimestampMap(audio)))
	}
}

// trackFile serves one of the item's stored track files, which skip and
// unskipped presentations share.
func (h hlsRequest) trackFile(file string) {
	switch file {
	case "audio.m4a":
		serveTrackFile(h.w, h.r, h.space, h.d.LocalPath, "audio/mp4")
	case "video.mp4":
		serveTrackFile(h.w, h.r, h.space, h.d.VideoPath, "video/mp4")
	case "clean.m4a":
		serveTrackFile(h.w, h.r, h.space, h.d.CleanTrackPath, "audio/mp4")
	default:
		http.NotFound(h.w, h.r)
	}
}

// captionCues are the item's captions as the presentation shows them:
// masked when the clean rendition plays. h.captions is false when there is
// nothing to mask with.
func (h hlsRequest) captionCues() ([]hls.Cue, error) {
	cues, err := readCaptions(h.space, h.d.CaptionsPath)
	if err != nil {
		return nil, err
	}
	if h.d.CleanTrackPath != "" {
		// The clean rendition is the one that plays, so its captions say
		// nothing the audio does not.
		prof := h.p.Profanity()
		for i := range cues {
			cues[i].Text = prof.FilterText(h.username, cues[i].Text)
		}
	}
	return cues, nil
}

// serveTrackFile serves a stored track file with byte ranges; 404 when the
// item has none.
func serveTrackFile(w http.ResponseWriter, r *http.Request, space Space, stored, mime string) {
	if stored == "" {
		http.NotFound(w, r)
		return
	}
	serveFile(w, r, space.Own, Name(stored), mime)
}

// serveFile serves a file of from with byte ranges.
func serveFile(w http.ResponseWriter, r *http.Request, from plugin.Storage, name, mime string) {
	f, err := from.Open(name)
	if err != nil {
		hlsError(w, r, err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			slog.Warn("feed hls: close track", "path", name, "err", err)
		}
	}()
	st, err := f.Stat()
	if err != nil {
		hlsError(w, r, err)
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

// hlsError answers a request that failed: a file gone from the storage or
// the cache (evicted, or removed under a stale row) is not found, and
// anything else is the server's.
func hlsError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, fs.ErrNotExist) {
		slog.Warn("feed hls: file missing", "path", r.URL.Path, "err", err)
		http.NotFound(w, r)
		return
	}
	httperr.Internal(w, r, err)
}
