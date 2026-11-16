package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/ffargs"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// Edges makes the edge pieces of the skip presentations players asked for
// (feed.SkipPlan, Store.WantEdges): every part of a segment that a cut
// leaves is re-encoded into a track file of its own in the cache,
// feed.EdgeName. Each rendition is planned and cut on its own segment grid.
// Audio is cut to the sample; H.264 video is re-encoded from the keyframe
// before the cut. A presentation with a rendition whose edges cannot be
// made (feed.EdgesSupported) is not skipped, so it gets no edges at all.
//
// It runs on the goroutine every user's workers share, so one run takes a
// few items and stops after a time budget, between edges. What it made is
// kept, when it runs out of time or is cancelled: the next run checks it
// and goes on from there.
//
// An edge must decode under its source's init section, and its source
// under an edge's: ffmpeg's HLS demuxer (mpv) keeps the first moov it sees
// and feeds every segment to one decoder. So an edge keeps the source's
// timescale, AAC sample rate and channels, and timestamps (item time); a
// video edge repeats its SPS and PPS in band under an id of its own
// (edgeParamSetID), and its init carries the source's sets as well
// (hls.AddParamSets).
type Edges struct {
	store *feed.Store
	space feed.Space
	// afterEncode, when set, runs between encoding and recording (tests).
	afterEncode func()
	// afterEdge, when set, runs after each edge is made (tests).
	afterEdge func()
	limit     int           // items a run takes
	budget    time.Duration // how long a run encodes
}

// A run's default budget: an hour-long episode has hundreds of edges per
// rendition, and a playlist may want many episodes.
const (
	edgesPerRun    = 2
	edgesRunBudget = 60 * time.Second
)

// videoSeekEarly is how far before a video piece an edge seeks: less than
// any frame's duration.
const videoSeekEarly = 1e-4

// edgeParamSetID is the SPS and PPS id of a video edge's in-band parameter
// sets. ffmpeg's HLS demuxer (mpv) feeds every segment to one decoder: an
// edge's sets under the source's id (0, as encoders write it) would replace
// the source's, and the kept segments after it would decode under them.
// Under another id both are known, and the edge's init carries the
// source's too (hls.AddParamSets), for when it is the init ffmpeg keeps.
const edgeParamSetID = 1

// errOutOfTime stops a run between edges; the item stays pending.
var errOutOfTime = errors.New("edges: out of time")

// NewEdges creates an Edges worker over the downloads in space.
func NewEdges(s *feed.Store, space feed.Space) *Edges {
	return &Edges{store: s, space: space, limit: edgesPerRun, budget: edgesRunBudget}
}

// RunOnce makes the edges of the first few items that want them and have
// none, for as long as its budget lasts.
func (w *Edges) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingEdges(w.limit)
	if err != nil {
		return fmt.Errorf("edges list pending: %w", err)
	}
	deadline := time.Now().Add(w.budget)
	for i, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		// The first item always gets an edge, so every run makes progress.
		if i > 0 && time.Now().After(deadline) {
			return nil
		}
		err := w.processOne(ctx, id, deadline)
		if errors.Is(err, errOutOfTime) {
			slog.Info("feed edges: out of time; going on next run", "item_id", id)
			return nil
		}
		if err != nil && ctx.Err() != nil {
			return err
		}
		if err != nil {
			slog.Warn("feed edges: process failed", "item_id", id, "err", err)
		}
	}
	return nil
}

// rendition is one track of a presentation, as the plan cuts it.
type rendition struct {
	key    string // feed.EdgeOriginal, EdgeClean or EdgeVideo
	stored string
	track  hls.Track
	pieces []hls.Piece
}

// processOne makes one item's edges. A failure is recorded against the
// edit, so the item is not tried again until an input changes.
func (w *Edges) processOne(ctx context.Context, itemID int64, deadline time.Time) error {
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	plan, rs, err := w.load(d)
	if err != nil {
		return w.fail(itemID, "", err)
	}
	dir := path.Join(feed.EdgesDir(itemID), plan.Edit)
	if err := w.make(ctx, itemID, plan.Edit, rs, deadline); err != nil {
		if errors.Is(err, errOutOfTime) || ctx.Err() != nil {
			// Keep what was made: the next run checks it (made) and goes
			// on, and Collect keeps a pending item's edges.
			return err
		}
		return w.fail(itemID, plan.Edit, errors.Join(err, w.space.Cache.RemoveAll(dir)))
	}
	if w.afterEncode != nil {
		w.afterEncode()
	}
	// An input may have changed while the edges were encoded (a new
	// download, package, clean track, silence ranges or pref). Its reset
	// came before this record would, so recording now would keep stale
	// edges: check the plan again, and leave the item for the next run.
	stale, err := w.stale(itemID, plan.Edit)
	if err != nil || stale {
		rmErr := w.space.Cache.RemoveAll(dir)
		if err != nil {
			// Recorded, or every run would encode the item again.
			return errors.Join(w.fail(itemID, plan.Edit, fmt.Errorf("check plan again: %w", err)), rmErr)
		}
		slog.Info("feed edges: inputs changed while encoding; made again next run", "item_id", itemID, "edit", plan.Edit)
		return rmErr
	}
	index, err := w.index(itemID, plan.Edit, rs)
	if err != nil {
		return errors.Join(w.fail(itemID, plan.Edit, err), w.space.Cache.RemoveAll(dir))
	}
	if err := w.store.SetEdges(itemID, plan.Edit, index); err != nil {
		return errors.Join(err, w.space.Cache.RemoveAll(dir))
	}
	return w.removeOthers(itemID, plan.Edit)
}

func (w *Edges) fail(itemID int64, edit string, err error) error {
	if merr := w.store.MarkEdgesError(itemID, edit, err.Error()); merr != nil {
		return errors.Join(err, merr)
	}
	return err
}

// load reads d's tracks and plans them. An Unsupported plan is not
// skipped, so it has no renditions to cut.
func (w *Edges) load(d feed.Download) (feed.EdgePlan, []rendition, error) {
	original, _, err := w.space.Track(d.LocalPath)
	if err != nil {
		return feed.EdgePlan{}, nil, fmt.Errorf("original track: %w", err)
	}
	var clean, video *hls.Track
	if d.CleanTrackPath != "" {
		t, _, err := w.space.Track(d.CleanTrackPath)
		if err != nil {
			return feed.EdgePlan{}, nil, fmt.Errorf("clean track: %w", err)
		}
		clean = &t
	}
	if d.VideoPath != "" {
		t, _, err := w.space.Track(d.VideoPath)
		if err != nil {
			return feed.EdgePlan{}, nil, fmt.Errorf("video track: %w", err)
		}
		video = &t
	}
	plan, err := feed.SkipPlan(w.store, d, original, clean, video)
	if err != nil {
		return feed.EdgePlan{}, nil, fmt.Errorf("skip plan: %w", err)
	}
	if plan.Unsupported {
		return plan, nil, nil
	}
	rs := []rendition{{feed.EdgeOriginal, d.LocalPath, original, plan.Original}}
	if clean != nil {
		rs = append(rs, rendition{feed.EdgeClean, d.CleanTrackPath, *clean, plan.Clean})
	}
	if video != nil {
		rs = append(rs, rendition{feed.EdgeVideo, d.VideoPath, *video, plan.Video})
	}
	return plan, rs, nil
}

// index reads where the media of each edge of the edit is, for playlists
// to list without opening the files.
func (w *Edges) index(itemID int64, edit string, rs []rendition) (feed.EdgesIndex, error) {
	index := feed.EdgesIndex{}
	for _, r := range rs {
		for n := range hls.Edges(r.pieces) {
			name := feed.EdgeName(itemID, edit, r.key, n)
			tr, _, err := w.space.CachedTrack(name)
			if err != nil {
				return nil, fmt.Errorf("index edge: %w", err)
			}
			e, ok := feed.EdgeEntryOf(tr)
			if !ok {
				return nil, fmt.Errorf("index edge %s: no fragments", name)
			}
			index[r.key] = append(index[r.key], e)
		}
	}
	return index, nil
}

// stale reports whether the item's plan is no longer edit, or it is no
// longer waiting for its edges.
func (w *Edges) stale(itemID int64, edit string) (bool, error) {
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return false, fmt.Errorf("get download: %w", err)
	}
	if d.PackageState != "done" || d.SilenceState != "done" || d.EdgesEdit != "" || d.EdgesError != "" {
		return true, nil
	}
	plan, _, err := w.load(d)
	if err != nil {
		return false, err
	}
	return plan.Edit != edit, nil
}

// make encodes every edge piece of every rendition into the edit's
// directory and checks it. An edge an earlier run made that passes the
// check is kept. It stops with errOutOfTime once an edge is made after the
// deadline.
func (w *Edges) make(ctx context.Context, itemID int64, edit string, rs []rendition, deadline time.Time) error {
	for _, r := range rs {
		edges := hls.Edges(r.pieces)
		if len(edges) == 0 {
			continue
		}
		in, err := w.space.Path(r.stored)
		if err != nil {
			return fmt.Errorf("media path: %w", err)
		}
		src, err := w.source(ctx, in, r)
		if err != nil {
			return err
		}
		for n, piece := range edges {
			name := feed.EdgeName(itemID, edit, r.key, n)
			if w.made(name, piece, src) {
				continue
			}
			err := w.encode(ctx, in, name, piece, src)
			if err == nil {
				err = w.check(name, piece, src)
			}
			if err != nil {
				return fmt.Errorf("edge %s (%.3f-%.3f s of %s): %w", name, piece.Start, piece.End, r.stored, err)
			}
			if w.afterEdge != nil {
				w.afterEdge()
			}
			if time.Now().After(deadline) {
				return errOutOfTime
			}
		}
	}
	return nil
}

// edgeSource is what an edge must match of the track it is cut from.
type edgeSource struct {
	Kind   hls.Kind
	Codecs string
	// T0 is the track's first fragment start: ffmpeg seeks from there.
	T0                   float64
	SampleRate, Channels int     // audio
	Timescale            int     // video
	Frame                float64 // one frame's duration, s
	// ParamSets are a video track's SPS and PPS, which its edges' inits
	// carry too.
	ParamSets hls.ParamSets
	// Delay is how far a video track's decode times run behind its
	// presentation times: its first frame's time (T0) less its first tfdt,
	// the B-frame delay of its encoder.
	Delay float64
}

// source probes a rendition's track for what its edges must match.
func (w *Edges) source(ctx context.Context, in string, r rendition) (edgeSource, error) {
	if len(r.track.Fragments) == 0 {
		return edgeSource{}, fmt.Errorf("%s has no fragments", r.stored)
	}
	args := append([]string{"-v", "error", "-select_streams", "0",
		"-show_entries", "stream=codec_type,sample_rate,channels,time_base,avg_frame_rate,r_frame_rate",
		"-of", "json"}, ffargs.Probe(in)...)
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{in}}, "ffprobe", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return edgeSource{}, fmt.Errorf("ffprobe %s: %w: %s", r.stored, err, strings.TrimSpace(stderr.String()))
	}
	src, err := parseEdgeProbe(out, r.track.Kind)
	if err != nil {
		return edgeSource{}, fmt.Errorf("ffprobe %s: %w", r.stored, err)
	}
	src.Codecs = r.track.Codecs
	src.T0 = r.track.Fragments[0].Start
	if src.Kind == hls.Video {
		init, err := w.space.TrackInit(r.stored, r.track)
		if err != nil {
			return edgeSource{}, fmt.Errorf("init of %s: %w", r.stored, err)
		}
		if src.ParamSets, err = hls.InitParamSets(init); err != nil {
			return edgeSource{}, fmt.Errorf("parameter sets of %s: %w", r.stored, err)
		}
		tfdt, err := w.space.DecodeTime(r.stored)
		if err != nil {
			return edgeSource{}, fmt.Errorf("decode time of %s: %w", r.stored, err)
		}
		src.Delay = max(math.Round((src.T0-tfdt)*float64(src.Timescale))/float64(src.Timescale), 0)
	}
	return src, nil
}

// parseEdgeProbe reads ffprobe's JSON for a track's one stream of kind.
func parseEdgeProbe(out []byte, kind hls.Kind) (edgeSource, error) {
	var v struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			SampleRate   string `json:"sample_rate"`
			Channels     int    `json:"channels"`
			TimeBase     string `json:"time_base"`
			AvgFrameRate string `json:"avg_frame_rate"`
			RFrameRate   string `json:"r_frame_rate"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return edgeSource{}, fmt.Errorf("output: %w", err)
	}
	if len(v.Streams) != 1 || v.Streams[0].CodecType != string(kind) {
		return edgeSource{}, fmt.Errorf("want one %s stream, got %+v", kind, v.Streams)
	}
	s := v.Streams[0]
	src := edgeSource{Kind: kind}
	switch kind {
	case hls.Audio:
		rate, err := strconv.Atoi(s.SampleRate)
		if err != nil || rate <= 0 || s.Channels <= 0 {
			return edgeSource{}, fmt.Errorf("audio sample rate %q, %d channels", s.SampleRate, s.Channels)
		}
		src.SampleRate, src.Channels = rate, s.Channels
		src.Frame = 1024 / float64(rate) // an AAC-LC frame
	case hls.Video:
		num, den, ok := ratio(s.TimeBase)
		if !ok || num != 1 {
			return edgeSource{}, fmt.Errorf("video time base %q", s.TimeBase)
		}
		src.Timescale = den
		fps, per, ok := ratio(s.AvgFrameRate)
		if !ok {
			fps, per, ok = ratio(s.RFrameRate)
		}
		if !ok {
			return edgeSource{}, fmt.Errorf("video frame rate %q / %q", s.AvgFrameRate, s.RFrameRate)
		}
		src.Frame = float64(per) / float64(fps)
	}
	return src, nil
}

// ratio parses ffprobe's "a/b" with both positive.
func ratio(s string) (a, b int, ok bool) {
	x, y, found := strings.Cut(s, "/")
	if !found {
		return 0, 0, false
	}
	a, errA := strconv.Atoi(x)
	b, errB := strconv.Atoi(y)
	return a, b, errA == nil && errB == nil && a > 0 && b > 0
}

// made reports whether an earlier run made the edge and it passes check.
// One that does not is made again.
func (w *Edges) made(name string, piece hls.Piece, src edgeSource) bool {
	if _, err := w.space.Cache.Stat(name); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("feed edges: making an edge again: stat", "name", name, "err", err)
		}
		return false
	}
	if err := w.check(name, piece, src); err != nil {
		slog.Info("feed edges: making a kept edge again", "name", name, "err", err)
		return false
	}
	return true
}

// encode makes one edge.
func (w *Edges) encode(ctx context.Context, in, name string, piece hls.Piece, src edgeSource) error {
	out, err := w.space.Cache.LocalPath(name)
	if err != nil {
		return fmt.Errorf("edge path: %w", err)
	}
	args, err := edgeArgs(in, out, piece, src)
	if err != nil {
		return err
	}
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{in}, Write: []string{filepath.Dir(out)}}, "ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(string(b)))
	}
	if src.Kind == hls.Video {
		return w.addSourceSets(name, src)
	}
	return nil
}

// addSourceSets adds the source's parameter sets to the init of the video
// edge at name (hls.AddParamSets).
func (w *Edges) addSourceSets(name string, src edgeSource) error {
	f, err := w.space.Cache.Open(name)
	if err != nil {
		return fmt.Errorf("open edge: %w", err)
	}
	b, err := io.ReadAll(f)
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("read edge: %w", err)
	}
	b, err = hls.AddParamSets(b, src.ParamSets)
	if err != nil {
		return fmt.Errorf("edge init: %w", err)
	}
	if _, err := w.space.Cache.Put(name, bytes.NewReader(b)); err != nil {
		return fmt.Errorf("write edge: %w", err)
	}
	return nil
}

// check reports how the edge at name is not a track of the piece: one track
// with an index, of src's kind, starting, ending and lasting as the piece
// does to within a frame, placed where its index says (its first tfdt, by
// which players place it), and for video with its source's parameter sets
// in its init.
func (w *Edges) check(name string, piece hls.Piece, src edgeSource) error {
	tr, _, err := w.space.CachedTrack(name)
	if err != nil {
		return err
	}
	if tr.Kind != src.Kind {
		return fmt.Errorf("made a %s track, want %s", tr.Kind, src.Kind)
	}
	if len(tr.Fragments) == 0 {
		return errors.New("made a track with no fragments")
	}
	first, last := tr.Fragments[0], tr.Fragments[len(tr.Fragments)-1]
	if src.Kind == hls.Video && !first.Keyframe {
		return errors.New("made a video track that does not start with a keyframe")
	}
	// A video frame begins at or after the piece and may run past its end.
	// A video edge's index counts decode time, src.Delay before its frames.
	start, end := first.Start+src.Delay, last.Start+last.Duration+src.Delay
	tol := src.Frame + 1e-3
	if math.Abs(start-piece.Start) > tol || math.Abs(end-piece.End) > tol || math.Abs((end-start)-(piece.End-piece.Start)) > tol {
		return fmt.Errorf("made %.4f-%.4f s, want %.4f-%.4f s to within %.4f s", start, end, piece.Start, piece.End, tol)
	}
	tfdt, err := w.space.CachedDecodeTime(name)
	if err != nil {
		return err
	}
	if math.Abs(tfdt+src.Delay-start) > 1e-3 {
		return fmt.Errorf("first fragment decodes at %.4f s; its index says %.4f s", tfdt, start)
	}
	if src.Kind == hls.Video {
		return w.checkSourceSets(name, tr, src)
	}
	return nil
}

// checkSourceSets reports whether the init of the video edge at name lacks
// one of its source's parameter sets.
func (w *Edges) checkSourceSets(name string, tr hls.Track, src edgeSource) error {
	f, err := w.space.Cache.Open(name)
	if err != nil {
		return fmt.Errorf("open edge: %w", err)
	}
	init := make([]byte, tr.InitLength)
	_, err = f.Seek(tr.InitOffset, io.SeekStart)
	if err == nil {
		_, err = io.ReadFull(f, init)
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return fmt.Errorf("read edge init: %w", err)
	}
	sets, err := hls.InitParamSets(init)
	if err != nil {
		return fmt.Errorf("edge init: %w", err)
	}
	have := func(list [][]byte, s []byte) bool {
		return slices.ContainsFunc(list, func(x []byte) bool { return bytes.Equal(x, s) })
	}
	for _, s := range src.ParamSets.SPS {
		if !have(sets.SPS, s) {
			return errors.New("edge init lacks its source's SPS")
		}
	}
	for _, s := range src.ParamSets.PPS {
		if !have(sets.PPS, s) {
			return errors.New("edge init lacks its source's PPS")
		}
	}
	return nil
}

// removeOthers removes the item's edges of every other edit.
func (w *Edges) removeOthers(itemID int64, edit string) error {
	dir := feed.EdgesDir(itemID)
	entries, err := w.space.Cache.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list edges: %w", err)
	}
	for _, e := range entries {
		if e.Name() == edit {
			continue
		}
		if err := w.space.Cache.RemoveAll(path.Join(dir, e.Name())); err != nil {
			return fmt.Errorf("remove stale edges: %w", err)
		}
	}
	return nil
}

// edgeArgs is the ffmpeg command that cuts piece out of the track file in
// (src) into an edge at out.
//
// It seeks before the input, so audio is cut to the sample and video is
// decoded from the keyframe before the piece. Seeking counts from the
// track's own start (src.T0); -output_ts_offset puts the edge back in item
// time, and frag_discont writes that into the fragments' tfdt as well as the
// sidx (without it tfdt stays 0, and players place fragments by tfdt).
//
// Audio drops the encoder's priming frame (the packets before the input's
// start): the edge's edit list, which says to skip it, is lost under the
// init a player keeps, so it would play 23 ms of nothing per edge and
// overlap the piece before. Its first frame is then the piece's start,
// decoded with whatever came before it. -avoid_negative_ts
// make_non_negative stops the muxer moving it to 0.
//
// Video keeps the source's frame times (passthrough, at the demuxer's time
// base, in the source's timescale), seeking videoSeekEarly before the piece
// so a frame that starts exactly where the piece does is not lost to
// rounding. Its length is an output -t ending a millisecond short of the
// piece (an input -to keeps a frame up to 20 ms past it), so a frame that
// starts where the piece ends is left to what follows. Its decode times run
// src.Delay behind its frames, as the source's do (B-frames), so they never
// go back at a joint with the source, which ffmpeg answers by dropping
// frames: it has no B-frames itself (an encoder's delay would be its own,
// and the mp4 muxer would show every frame that much late), setts moves its
// decode times back by the delay, and its -output_ts_offset is where it
// seeked to less the delay, which the muxer adds back to its frames. Muxer
// shifts are off (-avoid_negative_ts disabled): the edge at the track's
// start decodes from the source's first decode time. Its parameter sets are
// edgeParamSetID's, which its source's slices never name.
//
// A change to what it makes must bump feed.EdgeFormat, or edges made the old
// way are served as the new.
func edgeArgs(in, out string, piece hls.Piece, src edgeSource) ([]string, error) {
	sec := func(x float64) string { return strconv.FormatFloat(x, 'f', 6, 64) }
	ss := max(piece.Start-src.T0, 0)
	if src.Kind == hls.Video {
		ss = max(ss-videoSeekEarly, 0)
	}
	head := []string{"-nostdin", "-y", "-v", "error", "-ss", sec(ss)}
	var tail []string
	switch src.Kind {
	case hls.Audio:
		if src.SampleRate <= 0 || src.Channels <= 0 {
			return nil, fmt.Errorf("audio edge: sample rate %d, %d channels", src.SampleRate, src.Channels)
		}
		head = append(head, "-to", sec(piece.End-src.T0))
		tail = []string{"-map", "0:a:0", "-c:a", "aac", "-b:a", "128k",
			"-ar", strconv.Itoa(src.SampleRate), "-ac", strconv.Itoa(src.Channels),
			"-bsf:a", `noise=drop=lt(pts\,0)`,
			"-output_ts_offset", sec(piece.Start), "-avoid_negative_ts", "make_non_negative",
			"-movflags", "+empty_moov+default_base_moof+global_sidx+frag_discont", "-frag_duration", "2000000", "-f", "mp4", out}
	case hls.Video:
		profile, level, ok := feed.X264Profile(src.Codecs)
		if !ok {
			return nil, fmt.Errorf("video edge: %q is not H.264 x264 can match", src.Codecs)
		}
		if src.Timescale <= 0 {
			return nil, fmt.Errorf("video edge: timescale %d", src.Timescale)
		}
		tail = []string{"-t", sec(piece.End - 1e-3 - (src.T0 + ss)), "-map", "0:V:0", "-an", "-fps_mode", "passthrough", "-enc_time_base", "demux",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-profile:v", profile, "-level", level,
			"-bf", "0", "-pix_fmt", "yuv420p", "-x264-params", "repeat-headers=1:sps-id=" + strconv.Itoa(edgeParamSetID),
			"-video_track_timescale", strconv.Itoa(src.Timescale),
			"-output_ts_offset", sec(src.T0 + ss - src.Delay), "-avoid_negative_ts", "disabled",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof+global_sidx+frag_discont", "-f", "mp4", out}
		if src.Delay > 0 {
			tail = append([]string{"-bsf:v", "setts=dts=DTS-round(" + sec(src.Delay) + "/TB)"}, tail...)
		}
	default:
		return nil, fmt.Errorf("edge of a %q track", src.Kind)
	}
	return append(append(head, ffargs.Input(in)...), tail...), nil
}
