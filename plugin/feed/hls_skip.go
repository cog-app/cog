package feed

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/httperr"

	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// EdgePlan is what a skip presentation of an item needs: the cuts it
// applies, each rendition planned against its own segment grid, and the
// edit id that names them. It is shared by the edges worker, which encodes
// the edge pieces, and the HLS route, which lists them.
type EdgePlan struct {
	Edit                   string
	Cuts                   []hls.Cut
	Original, Clean, Video []hls.Piece
	// Unsupported is a presentation with a rendition whose edges cannot be
	// made (see EdgesSupported): it is not skipped and has no pieces.
	Unsupported bool
}

// Rendition keys of EditID and EdgeName.
const (
	EdgeOriginal = "a"
	EdgeClean    = "c"
	EdgeVideo    = "v"
)

// SkipPlan plans the skip presentation of d's renditions: original, and clean
// and video when the presentation lists them (nil otherwise). The cuts are
// d's silence ranges through the user's shortest-silence pref, bounded by
// the end of original. A plan with no cuts still has an edit id; the route
// serves it unskipped. So it does a plan with a rendition whose edges
// cannot be made (Unsupported): cutting the others alone would put the
// renditions out of step.
func SkipPlan(store *Store, d Download, original hls.Track, clean, video *hls.Track) (EdgePlan, error) {
	ranges, err := d.silenceRanges()
	if err != nil {
		return EdgePlan{}, err
	}
	prefs, err := store.LoadPlayerPrefs()
	if err != nil {
		return EdgePlan{}, err
	}
	var silences []hls.Cut
	for _, r := range ranges {
		if r.Action == "skip" {
			silences = append(silences, hls.Cut{Start: r.Start, End: r.End})
		}
	}
	duration := 0.0
	if n := len(original.Fragments); n > 0 {
		last := original.Fragments[n-1]
		duration = last.Start + last.Duration
	}
	p := EdgePlan{Cuts: hls.EffectiveCuts(silences, prefs.SkipSilenceMinSec, duration)}
	layouts := map[string]hls.Track{EdgeOriginal: original}
	supported := EdgesSupported(original)
	if clean != nil {
		layouts[EdgeClean] = *clean
		supported = supported && EdgesSupported(*clean)
	}
	if video != nil {
		layouts[EdgeVideo] = *video
		supported = supported && EdgesSupported(*video)
	}
	p.Edit = hls.EditID(p.Cuts, layouts)
	if !supported {
		p.Unsupported = true
		return p, nil
	}
	p.Original = hls.PlanCuts(original, p.Cuts)
	if clean != nil {
		p.Clean = hls.PlanCuts(*clean, p.Cuts)
	}
	if video != nil {
		p.Video = hls.PlanCuts(*video, p.Cuts)
	}
	return p, nil
}

// EdgesSupported reports whether edges of t can be made that decode under
// t's own init section: AAC-LC audio (ffmpeg's encoder makes no other), and
// H.264 in a profile x264 can match (X264Profile).
func EdgesSupported(t hls.Track) bool {
	if t.Kind == hls.Audio {
		return t.Codecs == "mp4a.40.2"
	}
	_, _, ok := X264Profile(t.Codecs)
	return ok
}

// X264Profile is the x264 profile and level of an H.264 codecs string
// (avc1.PPCCLL or avc3.): baseline, main or high, and the level as "3.1".
// Other profiles (High 10, 4:2:2, 4:4:4) are not matched.
func X264Profile(codecs string) (profile, level string, ok bool) {
	rest, found := strings.CutPrefix(codecs, "avc1.")
	if !found {
		rest, found = strings.CutPrefix(codecs, "avc3.")
	}
	if !found || len(rest) != 6 {
		return "", "", false
	}
	b, err := hex.DecodeString(rest)
	if err != nil {
		return "", "", false
	}
	switch b[0] {
	case 0x42:
		profile = "baseline"
	case 0x4D:
		profile = "main"
	case 0x64:
		profile = "high"
	default:
		return "", "", false
	}
	if b[2] < 10 {
		return "", "", false
	}
	return profile, fmt.Sprintf("%d.%d", b[2]/10, b[2]%10), true
}

// EdgesDir is the cache directory holding an item's edges, one directory
// per edit id.
func EdgesDir(item int64) string {
	return fmt.Sprintf("edges/%d", item)
}

// EdgeName is the cache name of edge piece n of a rendition (EdgeOriginal,
// EdgeClean or EdgeVideo) of an edit.
func EdgeName(item int64, edit, track string, n int) string {
	return fmt.Sprintf("%s/%s/%s-%d.mp4", EdgesDir(item), edit, track, n)
}

// skipSelection is the presentation a skip request is served: the plan of
// each rendition it lists, and how the master describes it.
type skipSelection struct {
	// edit names the applied cuts; "" is the item unskipped.
	edit string
	cuts []hls.Cut
	// Each rendition's pieces; nil for one the presentation does not list.
	original, clean, video []hls.Piece
	// full is the plan with its edges; coarse drops whole segments only and
	// lists the default audio rendition alone.
	full, coarse bool
	// status is the us.duckfam.cog.skip session data: "pending" while the
	// edges are made, "unsupported" when they cannot be.
	status string
}

// skipOptions are the presentations a skip request may be served: the
// full plan, and for an audio-only item the coarse plan the master serves
// until the edges are made.
type skipOptions struct {
	plan EdgePlan
	// unskipped: no cuts apply (no silence, none long enough, or a cut
	// over the whole item), so the item plays as it is.
	unskipped bool
	// edges lists the edge pieces of each rendition key; ready is whether
	// they are made for plan, or there are none to make.
	edges map[string][]hls.Piece
	ready bool
	// coarse is the coarse selection; nil for video, or when no segment
	// is wholly cut.
	coarse *skipSelection
}

// skipOptions plans the skip presentations of the request's item. It only
// reads.
func (h hlsRequest) skipOptions(t itemTracks) (skipOptions, error) {
	var o skipOptions
	if h.d.SilenceState != "done" {
		o.unskipped = true
		return o, nil
	}
	plan, err := SkipPlan(h.store, h.d, t.original, t.clean, t.video)
	if err != nil {
		return o, err
	}
	o.plan = plan
	if len(plan.Cuts) == 0 || (!plan.Unsupported && len(plan.Original) == 0) {
		o.unskipped = true
		return o, nil
	}
	if plan.Unsupported {
		return o, nil
	}
	o.edges = map[string][]hls.Piece{EdgeOriginal: hls.Edges(plan.Original), EdgeClean: hls.Edges(plan.Clean), EdgeVideo: hls.Edges(plan.Video)}
	n := len(o.edges[EdgeOriginal]) + len(o.edges[EdgeClean]) + len(o.edges[EdgeVideo])
	o.ready = n == 0 || (h.d.EdgesEdit == plan.Edit && h.d.EdgesError == "")
	if t.video == nil {
		key, def := EdgeOriginal, t.original
		if t.clean != nil {
			key, def = EdgeClean, *t.clean
		}
		cuts := hls.CoarseCuts(def, plan.Cuts)
		if pieces := hls.PlanCuts(def, cuts); len(cuts) > 0 && len(pieces) > 0 {
			c := &skipSelection{edit: hls.EditID(cuts, map[string]hls.Track{key: def}), cuts: cuts, coarse: true}
			if key == EdgeClean {
				c.clean = pieces
			} else {
				c.original = pieces
			}
			o.coarse = c
		}
	}
	return o, nil
}

// full is the selection of the full plan.
func (o skipOptions) full() skipSelection {
	return skipSelection{edit: o.plan.Edit, cuts: o.plan.Cuts, original: o.plan.Original, clean: o.plan.Clean, video: o.plan.Video, full: true}
}

// masterSkip picks the presentation a skip master lists:
//   - unskipped, without session data, when no cuts apply;
//   - unskipped and "unsupported" when edges cannot be made;
//   - the full plan when its edges are in the cache, or it has none;
//   - otherwise the coarse plan of an audio-only item, or the video item
//     unskipped and "pending", while the edges worker is asked for them.
//
// An edit list it serves is recorded, so a player's report of it can be
// read back. Edges recorded for another plan, or recorded and since lost
// from the cache, are forgotten so the worker makes them again; edges that
// failed for this plan are not retried until an input changes.
func (h hlsRequest) masterSkip(t itemTracks) (skipSelection, error) {
	o, err := h.skipOptions(t)
	if err != nil {
		return skipSelection{}, err
	}
	switch {
	case o.unskipped:
		return skipSelection{}, nil
	case o.plan.Unsupported:
		return skipSelection{status: "unsupported"}, nil
	}
	item := h.d.ItemID
	if o.ready {
		missing, err := h.missingEdge(o)
		if err != nil {
			return skipSelection{}, err
		}
		if !missing {
			sel := o.full()
			return sel, h.store.RecordEdit(item, sel.edit, sel.cuts)
		}
	}
	healed := false
	if stale := h.d.EdgesEdit != "" && h.d.EdgesEdit != o.plan.Edit; stale || o.ready {
		// o.ready here is a made plan whose files are gone.
		slog.Info("feed hls: edges made again", "item", item, "had", h.d.EdgesEdit, "plan", o.plan.Edit)
		if err := h.store.ResetEdges(item); err != nil {
			return skipSelection{}, err
		}
		healed = true
	}
	wanted, err := h.store.WantEdges(item)
	if err != nil {
		return skipSelection{}, err
	}
	if wanted || healed {
		h.p.TriggerFetch(h.username, 0)
	}
	if o.coarse != nil {
		return *o.coarse, h.store.RecordEdit(item, o.coarse.edit, o.coarse.cuts)
	}
	return skipSelection{status: "pending"}, nil
}

// missingEdge reports whether an edge file of the plan is not in the cache.
func (h hlsRequest) missingEdge(o skipOptions) (bool, error) {
	for _, key := range []string{EdgeOriginal, EdgeClean, EdgeVideo} {
		for n := range o.edges[key] {
			name := EdgeName(h.d.ItemID, o.plan.Edit, key, n)
			if _, err := h.space.Cache.Stat(name); errors.Is(err, fs.ErrNotExist) {
				slog.Warn("feed hls: edge missing from the cache", "item", h.d.ItemID, "name", name)
				return true, nil
			} else if err != nil {
				return false, fmt.Errorf("feed hls: stat edge: %w", err)
			}
		}
	}
	return false, nil
}

// skipFile serves a file of the skip presentation the request's edit list
// names: the full plan once its edges are made, or the coarse plan. An
// edit list that is neither is not found.
func (h hlsRequest) skipFile(file string) {
	w, r := h.w, h.r
	if file == "audio.m4a" || file == "video.mp4" || file == "clean.m4a" {
		h.trackFile(file)
		return
	}
	tracks, err := loadTracks(h.space, h.d)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	o, err := h.skipOptions(tracks)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	var sel skipSelection
	switch {
	case o.unskipped || o.plan.Unsupported:
		http.NotFound(w, r)
		return
	case o.ready && h.opts.Edit == o.plan.Edit:
		sel = o.full()
	case o.coarse != nil && h.opts.Edit == o.coarse.edit:
		sel = *o.coarse
	default:
		http.NotFound(w, r)
		return
	}
	query := h.opts.query(h.sig)
	// The captions follow the default audio rendition's pieces.
	subsPieces := sel.original
	if tracks.clean != nil {
		subsPieces = sel.clean
	}
	switch file {
	case "audio.m3u8":
		h.cutPlaylist(tracks.original, sel, sel.original, EdgeOriginal, "audio.m4a"+query)
	case "clean.m3u8":
		if tracks.clean == nil {
			http.NotFound(w, r)
			return
		}
		h.cutPlaylist(*tracks.clean, sel, sel.clean, EdgeClean, "clean.m4a"+query)
	case "video.m3u8":
		if tracks.video == nil {
			http.NotFound(w, r)
			return
		}
		h.cutPlaylist(*tracks.video, sel, sel.video, EdgeVideo, "video.mp4"+query)
	case "subs.m3u8":
		if !h.captions || len(subsPieces) == 0 {
			http.NotFound(w, r)
			return
		}
		writePlaylist(w, r, hls.Playlist(hls.PieceSubtitleEntries(subsPieces, func(n int) string {
			return "subs-" + strconv.Itoa(n) + ".vtt" + query
		})))
	default:
		if n, ok := subsSegment(file); ok {
			if !h.captions || n >= len(subsPieces) {
				http.NotFound(w, r)
				return
			}
			cues, err := h.captionCues()
			if err != nil {
				httperr.Internal(w, r, err)
				return
			}
			// Cues stay in caption time, clipped to the piece: kept media
			// keeps its item-time timestamps, and hls.js places a cue after a
			// cut by its discontinuity's own offset (TestBrowser/HLSSkipCaptions).
			t0 := tracks.original.Fragments[0].Start
			p := subsPieces[n]
			writeVTT(w, r, hls.VTTPiece(cues, p.Start-t0, p.End-t0))
			return
		}
		edit, key, n, ok := edgeFile(file)
		if !ok || !sel.full || edit != sel.edit || n >= len(o.edges[key]) {
			http.NotFound(w, r)
			return
		}
		mime := "audio/mp4"
		if key == EdgeVideo {
			mime = "video/mp4"
		}
		serveFile(w, r, h.space.Cache, EdgeName(h.d.ItemID, edit, key, n), mime)
	}
}

// cutPlaylist writes the media playlist of a rendition under sel: its kept
// segments as byte ranges of fileURI, its edges from the cache. A
// rendition sel does not list is not found.
func (h hlsRequest) cutPlaylist(t hls.Track, sel skipSelection, pieces []hls.Piece, key, fileURI string) {
	if len(pieces) == 0 {
		http.NotFound(h.w, h.r)
		return
	}
	edgePieces := hls.Edges(pieces)
	edges := make([]hls.Track, len(edgePieces))
	for n := range edgePieces {
		tr, _, err := h.space.CachedTrack(EdgeName(h.d.ItemID, sel.edit, key, n))
		if err != nil {
			httperr.Internal(h.w, h.r, fmt.Errorf("feed hls: edge: %w", err))
			return
		}
		edges[n] = tr
	}
	query := h.opts.query(h.sig)
	entries, err := hls.CutEntries(t, pieces, fileURI, func(n int) string {
		return fmt.Sprintf("edge-%s-%s-%d.mp4%s", sel.edit, key, n, query)
	}, edges)
	if err != nil {
		httperr.Internal(h.w, h.r, fmt.Errorf("feed hls: item %d: %w", h.d.ItemID, err))
		return
	}
	writePlaylist(h.w, h.r, hls.Playlist(entries))
}

// edgeFile parses an edge's file name, edge-<edit>-<a|c|v>-<n>.mp4.
func edgeFile(file string) (edit, key string, n int, ok bool) {
	rest, found := strings.CutPrefix(file, "edge-")
	if !found {
		return "", "", 0, false
	}
	rest, found = strings.CutSuffix(rest, ".mp4")
	if !found {
		return "", "", 0, false
	}
	parts := strings.Split(rest, "-")
	if len(parts) != 3 || !validEditID(parts[0]) {
		return "", "", 0, false
	}
	switch parts[1] {
	case EdgeOriginal, EdgeClean, EdgeVideo:
	default:
		return "", "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], n, true
}
