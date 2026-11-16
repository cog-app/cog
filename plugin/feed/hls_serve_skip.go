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
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

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
		// o.ready here is a made plan whose edges are gone.
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

// missingEdge reports whether the plan's edges are gone from the cache: a
// plan with edges whose directory is missing. One Stat, so the master
// stays cheap; a single lost file is answered 404 when it is asked for.
func (h hlsRequest) missingEdge(o skipOptions) (bool, error) {
	if len(o.edges[EdgeOriginal])+len(o.edges[EdgeClean])+len(o.edges[EdgeVideo]) == 0 {
		return false, nil
	}
	dir := EdgesDir(h.d.ItemID) + "/" + o.plan.Edit
	if _, err := h.space.Cache.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		slog.Warn("feed hls: edges missing from the cache", "item", h.d.ItemID, "dir", dir)
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("feed hls: stat edges: %w", err)
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
	if strings.HasPrefix(file, "edge-") {
		h.edge(file)
		return
	}
	tracks, err := loadTracks(h.space, h.d)
	if err != nil {
		hlsError(w, r, err)
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
	subsPieces, subsTrack := sel.original, tracks.original
	if tracks.clean != nil {
		subsPieces, subsTrack = sel.clean, *tracks.clean
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
				hlsError(w, r, err)
				return
			}
			// Cues stay in caption time, clipped to the piece, under the
			// unskipped presentation's map (the Original's, which a clean
			// track starts with too): kept media and edges keep their media
			// time, and hls.js places a cue after a cut by its
			// discontinuity's own offset (TestBrowser/HLSSkipCaptions).
			t0 := subsTrack.Fragments[0].Start
			p := subsPieces[n]
			writeVTT(w, r, hls.VTTPiece(cues, p.Start-t0, p.End-t0, hls.TimestampMap(tracks.original)))
			return
		}
		http.NotFound(w, r)
	}
}

// edge serves an edge file of the edit list the request names. It is
// checked against the edges recorded as made, without planning: players
// fetch every edge, and the playlist that named it checked the plan.
func (h hlsRequest) edge(file string) {
	edit, key, n, ok := edgeFile(file)
	if !ok || edit != h.opts.Edit || edit != h.d.EdgesEdit || h.d.EdgesError != "" {
		http.NotFound(h.w, h.r)
		return
	}
	index, err := h.d.edgesIndex()
	if err != nil {
		httperr.Internal(h.w, h.r, err)
		return
	}
	if n >= len(index[key]) {
		http.NotFound(h.w, h.r)
		return
	}
	mime := "audio/mp4"
	if key == EdgeVideo {
		mime = "video/mp4"
	}
	serveFile(h.w, h.r, h.space.Cache, EdgeName(h.d.ItemID, edit, key, n), mime)
}

// cutPlaylist writes the media playlist of a rendition under sel: its kept
// segments as byte ranges of fileURI, its edges as the worker indexed them
// (CutEntries refuses an index that does not match the plan). A
// rendition sel does not list is not found.
func (h hlsRequest) cutPlaylist(t hls.Track, sel skipSelection, pieces []hls.Piece, key, fileURI string) {
	if len(pieces) == 0 {
		http.NotFound(h.w, h.r)
		return
	}
	var edges []hls.Track
	if sel.full {
		index, err := h.d.edgesIndex()
		if err != nil {
			httperr.Internal(h.w, h.r, err)
			return
		}
		for _, e := range index[key] {
			edges = append(edges, e.Track())
		}
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
