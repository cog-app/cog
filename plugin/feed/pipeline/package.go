package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ffargs"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// Packager makes every ready download HLS tracks: an audio track and, for a
// video, a video track, each a fragmented MP4 with a sidx (hls.ReadTrack).
// A file that already is one, as YouTube's tracks are, is used as it is.
// Anything else is remuxed with ffmpeg; video is never re-encoded, and audio
// is encoded to AAC only when it is something else.
type Packager struct {
	store *feed.Store
	space feed.Space
}

// NewPackager creates a Packager over the downloads in space.
func NewPackager(s *feed.Store, space feed.Space) *Packager {
	return &Packager{store: s, space: space}
}

// RunOnce packages every ready download not looked at yet.
func (p *Packager) RunOnce(ctx context.Context) error {
	ids, err := p.store.PendingPackage(100)
	if err != nil {
		return fmt.Errorf("package list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.processOne(ctx, id); err != nil {
			slog.Warn("feed package: process failed", "item_id", id, "err", err)
		}
	}
	return nil
}

// processOne packages one download. A failure is recorded, so the item keeps
// playing from its download and is not tried again every tick.
func (p *Packager) processOne(ctx context.Context, itemID int64) error {
	err := p.pack(ctx, itemID)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if merr := p.store.MarkPackageError(itemID, err.Error()); merr != nil {
		return errors.Join(err, merr)
	}
	return err
}

// track is one of a package's two tracks as it is being made.
type track struct {
	kind  hls.Kind
	path  string // the stored file the track is, once it is one
	size  int64
	ready bool
	// What it is made from, when it is not ready.
	source     string
	transcode  bool
	cached     string // the cache name ffmpeg writes
	kept       string // the storage name it was kept as
	needed     bool
	sourceDesc string
	// clean: a clean track, which starts at start, the Original's start.
	clean bool
	start float64
}

func (p *Packager) pack(ctx context.Context, itemID int64) (err error) {
	it, err := p.store.GetItem(itemID)
	if err != nil {
		return fmt.Errorf("get item: %w", err)
	}
	d, err := p.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	id := strconv.FormatInt(itemID, 10)
	audio := &track{kind: hls.Audio, needed: true, cached: "package/" + id + ".hls-audio.m4a"}
	video := &track{kind: hls.Video, needed: it.Type == "video", cached: "package/" + id + ".hls-video.mp4"}

	// Tracks that are already what HLS needs are used as they are.
	p.asIs(audio, d.LocalPath)
	p.asIs(video, d.VideoPath)
	if audio.ready && (!video.needed || video.ready) {
		return p.store.SavePackage(itemID, audio.path, video.path, audio.size+video.size)
	}

	defer func() {
		if err == nil {
			return
		}
		for _, t := range []*track{audio, video} {
			if t.source == "" {
				continue
			}
			err = errors.Join(err, p.space.Cache.Remove(t.cached))
			if t.kept != "" {
				err = errors.Join(err, p.space.Own.Remove(t.kept))
			}
		}
	}()

	if err := p.chooseSources(ctx, d, audio, video); err != nil {
		return err
	}
	for _, t := range []*track{audio, video} {
		if t.source == "" {
			continue
		}
		if err := p.remux(ctx, t); err != nil {
			return err
		}
	}
	return p.store.SavePackage(itemID, audio.path, video.path, audio.size+video.size)
}

// asIs marks t ready when the stored file is already a track of its kind:
// for audio, AAC.
func (p *Packager) asIs(t *track, stored string) {
	if stored == "" || !t.needed {
		return
	}
	tr, size, err := p.space.Track(stored)
	if err != nil || tr.Kind != t.kind {
		return
	}
	if t.kind == hls.Audio && !strings.HasPrefix(tr.Codecs, "mp4a.40.") {
		return
	}
	t.path, t.size, t.ready = stored, size, true
}

// chooseSources picks the stream each track not ready is made from. Video
// comes from the video file, else from the audio file (a merged download),
// skipping cover art; audio from the audio file, else the video file.
func (p *Packager) chooseSources(ctx context.Context, d feed.Download, audio, video *track) error {
	probed := map[string][]probeStream{}
	probe := func(stored string) ([]probeStream, error) {
		if s, ok := probed[stored]; ok {
			return s, nil
		}
		s, err := p.probe(ctx, stored)
		if err != nil {
			return nil, err
		}
		probed[stored] = s
		return s, nil
	}
	files := []string{d.LocalPath, d.VideoPath}
	if video.needed && !video.ready {
		for _, f := range []string{d.VideoPath, d.LocalPath} {
			if f == "" {
				continue
			}
			streams, err := probe(f)
			if err != nil {
				return err
			}
			if s, ok := firstStream(streams, "video"); ok {
				video.source = f
				video.sourceDesc = s.Codec
				break
			}
		}
		// A video item whose files hold no picture plays as audio.
	}
	if !audio.ready {
		for _, f := range files {
			if f == "" {
				continue
			}
			streams, err := probe(f)
			if err != nil {
				return err
			}
			if s, ok := firstStream(streams, "audio"); ok {
				audio.source = f
				audio.transcode = s.Codec != "aac"
				audio.sourceDesc = s.Codec
				break
			}
		}
		if audio.source == "" {
			return errors.New("the download has no audio stream")
		}
	}
	return nil
}

func firstStream(streams []probeStream, typ string) (probeStream, bool) {
	for _, s := range streams {
		if s.Type == typ && !s.AttachedPic {
			return s, true
		}
	}
	return probeStream{}, false
}

// probeStream is one stream ffprobe found.
type probeStream struct {
	Index       int
	Type, Codec string
	AttachedPic bool
}

// probe lists a stored file's streams.
func (p *Packager) probe(ctx context.Context, stored string) ([]probeStream, error) {
	in, err := p.space.Path(stored)
	if err != nil {
		return nil, fmt.Errorf("media path: %w", err)
	}
	args := append([]string{"-v", "error", "-show_entries", "stream=index,codec_type,codec_name:stream_disposition=attached_pic", "-of", "json"}, ffargs.Probe(in)...)
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{in}}, "ffprobe", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w: %s", stored, err, strings.TrimSpace(stderr.String()))
	}
	return parseProbe(out)
}

// parseProbe reads ffprobe's JSON (-of json) stream list.
func parseProbe(out []byte) ([]probeStream, error) {
	var v struct {
		Streams []struct {
			Index       int    `json:"index"`
			CodecName   string `json:"codec_name"`
			CodecType   string `json:"codec_type"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	streams := make([]probeStream, len(v.Streams))
	for i, s := range v.Streams {
		streams[i] = probeStream{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, AttachedPic: s.Disposition.AttachedPic != 0}
	}
	return streams, nil
}

// packageArgs is the ffmpeg command that makes in's first video (not cover
// art) or audio stream a fragmented MP4 track with a global sidx at out.
// testdata/media/make.sh makes its fixtures with the same flags.
func packageArgs(in, out string, kind hls.Kind, transcode bool) []string {
	args := append([]string{"-nostdin", "-y", "-v", "error"}, ffargs.Input(in)...)
	if kind == hls.Video {
		return append(args, "-map", "0:V:0", "-c", "copy",
			"-movflags", "+frag_keyframe+empty_moov+default_base_moof+global_sidx", "-f", "mp4", out)
	}
	args = append(args, "-map", "0:a:0")
	if transcode {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-c", "copy")
	}
	return append(args, "-movflags", "+empty_moov+default_base_moof+global_sidx", "-frag_duration", "2000000", "-f", "mp4", out)
}

// cleanTrackArgs is packageArgs for a clean track: an audio track that
// starts where the Original does (start, its first fragment's), in its sidx
// and its fragments' tfdt (frag_discont). The clean file's first sample is
// the Original's first, as the profanity worker decoded it, so its AAC
// priming frame, which a player that keeps the Original's init would never
// skip, is dropped (the packets before its start), as edges drop theirs:
// its first frame is then the Original's first.
func cleanTrackArgs(in, out string, transcode bool, start float64) []string {
	args := append([]string{"-nostdin", "-y", "-v", "error"}, ffargs.Input(in)...)
	args = append(args, "-map", "0:a:0")
	if transcode {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	} else {
		args = append(args, "-c", "copy")
	}
	return append(args, "-bsf:a", `noise=drop=lt(pts\,0)`,
		"-output_ts_offset", strconv.FormatFloat(start, 'f', 6, 64), "-avoid_negative_ts", "make_non_negative",
		"-movflags", "+empty_moov+default_base_moof+global_sidx+frag_discont", "-frag_duration", "2000000", "-f", "mp4", out)
}

// remux makes t from its source into the cache, checks the result has an
// index, and keeps it as media/<id>.hls-<kind>.
func (p *Packager) remux(ctx context.Context, t *track) error {
	in, err := p.space.Path(t.source)
	if err != nil {
		return fmt.Errorf("media path: %w", err)
	}
	out, err := p.space.Cache.LocalPath(t.cached)
	if err != nil {
		return fmt.Errorf("package path: %w", err)
	}
	args := packageArgs(in, out, t.kind, t.transcode)
	if t.clean {
		args = cleanTrackArgs(in, out, t.transcode, t.start)
	}
	cmd := sandbox.CommandContext(ctx, sandbox.Spec{Read: []string{in}, Write: []string{filepath.Dir(out)}}, "ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg %s track from %s (%s): %w: %s", t.kind, t.source, t.sourceDesc, err, strings.TrimSpace(string(b)))
	}
	name := "media/" + strings.TrimPrefix(t.cached, "package/")
	size, err := p.space.Keep(name, t.cached)
	if err != nil {
		return fmt.Errorf("keep %s: %w", name, err)
	}
	t.kept = name
	tr, _, err := p.space.Track(name)
	if err != nil {
		return fmt.Errorf("packaged %s track: %w", t.kind, err)
	}
	if tr.Kind != t.kind {
		return fmt.Errorf("packaged %s track holds %s", t.kind, tr.Kind)
	}
	t.path, t.size = name, size
	return nil
}

// RunCleanOnce makes every packaged download's cleaned audio an HLS audio
// track, media/<id>.hls-audio.clean.m4a.
func (p *Packager) RunCleanOnce(ctx context.Context) error {
	ids, err := p.store.PendingCleanPackage(100)
	if err != nil {
		return fmt.Errorf("package clean list pending: %w", err)
	}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := p.cleanOne(ctx, id); err != nil {
			slog.Warn("feed package: clean track failed", "item_id", id, "err", err)
		}
	}
	return nil
}

// cleanOne packages one download's clean audio. A failure is recorded, so the
// item keeps playing the clean file progressively and is not tried again
// every tick.
func (p *Packager) cleanOne(ctx context.Context, itemID int64) error {
	err := p.packClean(ctx, itemID)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if merr := p.store.MarkCleanTrackError(itemID, err.Error()); merr != nil {
		return errors.Join(err, merr)
	}
	return err
}

// packClean makes the clean file an audio track whatever it is: AAC is
// copied, anything else encoded, and a video stream is left out.
func (p *Packager) packClean(ctx context.Context, itemID int64) (err error) {
	d, err := p.store.GetDownload(itemID)
	if err != nil {
		return fmt.Errorf("get download: %w", err)
	}
	streams, err := p.probe(ctx, d.CleanAudioPath)
	if err != nil {
		return err
	}
	s, ok := firstStream(streams, "audio")
	if !ok {
		return errors.New("the clean audio has no audio stream")
	}
	original, _, err := p.space.Track(d.LocalPath)
	if err != nil {
		return fmt.Errorf("original track: %w", err)
	}
	if len(original.Fragments) == 0 {
		return errors.New("the original track has no fragments")
	}
	t := &track{
		kind:       hls.Audio,
		needed:     true,
		source:     d.CleanAudioPath,
		transcode:  s.Codec != "aac",
		sourceDesc: s.Codec,
		cached:     "package/" + strconv.FormatInt(itemID, 10) + ".hls-audio.clean.m4a",
		clean:      true,
		start:      original.Fragments[0].Start,
	}
	defer func() {
		if err == nil {
			return
		}
		err = errors.Join(err, p.space.Cache.Remove(t.cached))
		if t.kept != "" {
			err = errors.Join(err, p.space.Own.Remove(t.kept))
			// The track replaced the clean file itself (cleaned before clean
			// tracks existed), so the file the row names is gone: clean again.
			if t.kept == d.CleanAudioPath {
				err = errors.Join(err, p.store.ResetCleanAudio(itemID))
			}
		}
	}()
	if err := p.remux(ctx, t); err != nil {
		return err
	}
	return p.store.SaveCleanTrack(itemID, t.path)
}
