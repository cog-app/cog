package pipeline

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"path"
	"path/filepath"
	"strconv"
	"strings"

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
// before the cut. A presentation whose video is anything else is not
// skipped, so it gets no edges at all.
//
// An edge must decode under its source's init section as well as its own:
// ffmpeg's HLS demuxer (mpv) keeps the first moov it sees. So it keeps the
// source's timescale, AAC sample rate and channels, and repeats the SPS and
// PPS in band, and its timestamps are the source's (item time).
type Edges struct {
	store *feed.Store
	space feed.Space
	// afterEncode, when set, runs between encoding and recording (tests).
	afterEncode func()
}

// NewEdges creates an Edges worker over the downloads in space.
func NewEdges(s *feed.Store, space feed.Space) *Edges {
	return &Edges{store: s, space: space}
}

// RunOnce makes the edges of every item that wants them and has none.
func (w *Edges) RunOnce(ctx context.Context) error {
	ids, err := w.store.PendingEdges(100)
	if err != nil {
		return fmt.Errorf("edges list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := w.processOne(ctx, id); err != nil {
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
func (w *Edges) processOne(ctx context.Context, itemID int64) error {
	d, err := w.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	plan, rs, err := w.load(d)
	if err != nil {
		return w.fail(itemID, "", err)
	}
	dir := path.Join(feed.EdgesDir(itemID), plan.Edit)
	if err := w.make(ctx, itemID, plan.Edit, rs); err != nil {
		err = errors.Join(err, w.space.Cache.RemoveAll(dir))
		if ctx.Err() != nil {
			return err
		}
		return w.fail(itemID, plan.Edit, err)
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
			return errors.Join(fmt.Errorf("check plan again: %w", err), rmErr)
		}
		slog.Info("feed edges: inputs changed while encoding; made again next run", "item_id", itemID, "edit", plan.Edit)
		return rmErr
	}
	if err := w.store.SetEdges(itemID, plan.Edit); err != nil {
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

// load reads d's tracks and plans them. A presentation whose video is not
// H.264 is not skipped, so it has no renditions to cut.
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
	if plan.VideoUnsupported {
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

// make encodes and checks every edge piece of every rendition into the
// edit's directory, which it empties first.
func (w *Edges) make(ctx context.Context, itemID int64, edit string, rs []rendition) error {
	if err := w.space.Cache.RemoveAll(path.Join(feed.EdgesDir(itemID), edit)); err != nil {
		return fmt.Errorf("clear edges: %w", err)
	}
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
			if err := w.encode(ctx, in, name, piece, src); err != nil {
				return fmt.Errorf("edge %s (%.3f-%.3f s of %s): %w", name, piece.Start, piece.End, r.stored, err)
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

// encode makes one edge and checks it is a track of the piece: one track
// with an index, of src's kind, starting, ending and lasting as the piece
// does to within a frame.
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
	// AAC puts its priming frame before the piece, and a video frame
	// begins at or after the piece and may run past its end.
	start, end := first.Start, last.Start+last.Duration
	tol := src.Frame + 1e-3
	if math.Abs(start-piece.Start) > tol || math.Abs(end-piece.End) > tol || math.Abs((end-start)-(piece.End-piece.Start)) > tol {
		return fmt.Errorf("made %.4f-%.4f s, want %.4f-%.4f s to within %.4f s", start, end, piece.Start, piece.End, tol)
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
// time, which -avoid_negative_ts disabled keeps: the mp4 muxer would
// otherwise move it to 0. Video keeps the source's frame times
// (passthrough, at the demuxer's time base, in the source's timescale), and
// its length is an output -t a millisecond short of the piece (an input -to
// keeps a frame up to 20 ms past it), so a frame that starts where the
// piece ends is left to what follows.
func edgeArgs(in, out string, piece hls.Piece, src edgeSource) ([]string, error) {
	sec := func(x float64) string { return strconv.FormatFloat(x, 'f', 3, 64) }
	ss := max(piece.Start-src.T0, 0)
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
			"-output_ts_offset", sec(piece.Start), "-avoid_negative_ts", "disabled",
			"-movflags", "+empty_moov+default_base_moof+global_sidx", "-frag_duration", "2000000", "-f", "mp4", out}
	case hls.Video:
		profile, level, ok := x264Profile(src.Codecs)
		if !ok {
			return nil, fmt.Errorf("video edge: %q is not H.264 x264 can match", src.Codecs)
		}
		if src.Timescale <= 0 {
			return nil, fmt.Errorf("video edge: timescale %d", src.Timescale)
		}
		tail = []string{"-t", sec(piece.End - piece.Start - 1e-3), "-map", "0:V:0", "-an", "-fps_mode", "passthrough", "-enc_time_base", "demux",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "20", "-profile:v", profile, "-level", level,
			"-pix_fmt", "yuv420p", "-x264-params", "repeat-headers=1",
			"-video_track_timescale", strconv.Itoa(src.Timescale),
			"-output_ts_offset", sec(piece.Start), "-avoid_negative_ts", "disabled",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof+global_sidx", "-f", "mp4", out}
	default:
		return nil, fmt.Errorf("edge of a %q track", src.Kind)
	}
	return append(append(head, ffargs.Input(in)...), tail...), nil
}

// x264Profile is the x264 profile and level of an H.264 codecs string
// (avc1.PPCCLL): baseline, main or high, and the level as "3.1". Other
// profiles are not matched.
func x264Profile(codecs string) (profile, level string, ok bool) {
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
