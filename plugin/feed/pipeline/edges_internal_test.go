package pipeline

import (
	"math"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// pair reports whether args holds flag immediately followed by value.
func pair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestEdgeArgsAudio(t *testing.T) {
	src := edgeSource{Kind: hls.Audio, Codecs: "mp4a.40.2", SampleRate: 44100, Channels: 2, Frame: 1024.0 / 44100}
	// 146 frames.
	end := 2.6 + 146*1024.0/44100
	args, err := edgeArgs("/in.m4a", "/out.mp4", hls.Piece{Start: 2.6, End: end, Edge: true}, src)
	require.NoError(t, err)
	for _, p := range [][2]string{
		// It ends on the sample; the input runs a frame further.
		{"-ss", "2.600000"}, {"-to", "6.013333"}, {"-af", "atrim=end_sample=149504"},
		{"-output_ts_offset", "2.600000"},
		{"-bsf:a", `noise=drop=lt(pts\,0)`},
		{"-c:a", "aac"}, {"-ar", "44100"}, {"-ac", "2"}, {"-map", "0:a:0"},
		{"-avoid_negative_ts", "make_non_negative"}, {"-f", "mp4"},
	} {
		assert.True(t, pair(args, p[0], p[1]), "%s %s in %q", p[0], p[1], args)
	}
	// Item time goes into the tfdt as well as the sidx.
	i := slices.Index(args, "-movflags")
	require.True(t, i >= 0, "%q", args)
	assert.True(t, strings.Contains(args[i+1], "frag_discont"), args[i+1])
	assert.False(t, slices.Contains(args, "-c:v"), "%q", args)
	assert.Equal(t, "/out.mp4", args[len(args)-1])
	// Seeking comes before the input, so it is exact and fast.
	assert.True(t, slices.Index(args, "-ss") < slices.Index(args, "-i"), "%q", args)
}

// A track that starts after 0 is seeked from its own start; the output
// keeps item time.
func TestEdgeArgsOffsetTrack(t *testing.T) {
	src := edgeSource{Kind: hls.Audio, T0: 1.5, SampleRate: 48000, Channels: 1, Frame: 1024.0 / 48000}
	args, err := edgeArgs("/in.m4a", "/out.mp4", hls.Piece{Start: 4.1, End: 6, Edge: true}, src)
	require.NoError(t, err)
	assert.True(t, pair(args, "-ss", "2.600000"), "%q", args)
	assert.True(t, pair(args, "-to", "4.521333"), "%q", args)
	assert.True(t, pair(args, "-output_ts_offset", "4.100000"), "%q", args)
}

func TestEdgeArgsVideo(t *testing.T) {
	src := edgeSource{Kind: hls.Video, Codecs: "avc1.42C01E", Timescale: 12288}
	args, err := edgeArgs("/in.mp4", "/out.mp4", hls.Piece{Start: 4.1, End: 6, Edge: true}, src)
	require.NoError(t, err)
	for _, p := range [][2]string{
		{"-profile:v", "baseline"}, {"-level", "3.0"}, {"-map", "0:V:0"}, {"-c:v", "libx264"},
		// Parameter sets of their own, which the source's slices never name.
		{"-x264-params", "repeat-headers=1:sps-id=1"}, {"-video_track_timescale", "12288"},
		{"-bf", "0"}, {"-fps_mode", "passthrough"},
		{"-enc_time_base", "demux"}, {"-pix_fmt", "yuv420p"}, {"-avoid_negative_ts", "disabled"},
		// A tenth of a millisecond early, so a frame at the piece's start
		// is not lost to rounding; the offset is where it seeked to.
		{"-ss", "4.099900"}, {"-output_ts_offset", "4.099900"},
	} {
		assert.True(t, pair(args, p[0], p[1]), "%s %s in %q", p[0], p[1], args)
	}
	// A frame that starts where the piece ends belongs to what follows.
	assert.True(t, pair(args, "-t", "1.899100"), "%q", args)
	assert.False(t, slices.Contains(args, "-bsf:v"), "no delay, no setts: %q", args)
	assert.True(t, slices.Index(args, "-t") > slices.Index(args, "-i"), "-t is an output option: %q", args)
	assert.False(t, slices.Contains(args, "-to"), "%q", args)
	assert.True(t, slices.Contains(args, "-an"), "%q", args)
	i := slices.Index(args, "-movflags")
	require.True(t, i >= 0, "%q", args)
	assert.True(t, strings.Contains(args[i+1], "frag_keyframe"), args[i+1])
	assert.True(t, strings.Contains(args[i+1], "global_sidx"), args[i+1])
	assert.True(t, strings.Contains(args[i+1], "frag_discont"), args[i+1])

	_, err = edgeArgs("/in.mp4", "/out.mp4", hls.Piece{Start: 4.1, End: 6}, edgeSource{Kind: hls.Video, Codecs: "vp09.00.40.08", Timescale: 1000})
	assert.Error(t, err)
	_, err = edgeArgs("/in.mp4", "/out.mp4", hls.Piece{Start: 4.1, End: 6}, edgeSource{Kind: hls.Video, Codecs: "avc1.42C01E"})
	assert.Error(t, err, "no timescale")
}

// A source whose decode times run behind its frames (B-frames) gets edges
// whose decode times run as far behind, so they never go back at a joint:
// the offset starts decoding that much early, and setts moves only the
// decode times, leaving every frame at its source time.
func TestEdgeArgsVideoDelay(t *testing.T) {
	src := edgeSource{Kind: hls.Video, Codecs: "avc1.640028", Timescale: 90000, T0: 1.0 / 12, Delay: 1.0 / 12}
	args, err := edgeArgs("/in.mp4", "/out.mp4", hls.Piece{Start: 4.1, End: 6, Edge: true}, src)
	require.NoError(t, err)
	assert.True(t, pair(args, "-ss", "4.016567"), "%q", args)
	assert.True(t, pair(args, "-output_ts_offset", "4.016567"), "%q", args)
	assert.True(t, pair(args, "-bsf:v", "setts=dts=DTS-round(0.083333/TB)"), "%q", args)
	assert.True(t, pair(args, "-t", "1.899100"), "%q", args)
}

// Audio edges are whole AAC frames, so each is up to a frame longer or
// shorter than its piece; a player that keeps time by the audio it plays
// (mpv) would drift by the sum. Each edge takes the frame count that keeps
// the audio played so far within half a frame of the plan, and keeps the
// end that joins the media beside it: its start, unless what follows it
// follows without a cut.
func TestAudioSpans(t *testing.T) {
	frame := 1024.0 / 44100
	var pieces []hls.Piece
	// Segments of 6 s; in each, a cut from 2.3+k/7 s to 3.9 s, and in every
	// other one a second cut from 4.6 to 5.2 s.
	for seg := range 12 {
		s := float64(seg) * 6
		pieces = append(pieces, hls.Piece{Start: s, End: s + 2.3 + float64(seg)/7, Segment: seg, Edge: true})
		if seg%2 == 0 {
			pieces = append(pieces, hls.Piece{Start: s + 3.9, End: s + 4.6, Segment: seg, Edge: true})
			pieces = append(pieces, hls.Piece{Start: s + 5.2, End: s + 6, Segment: seg, Edge: true})
		} else {
			pieces = append(pieces, hls.Piece{Start: s + 3.9, End: s + 6, Segment: seg, Edge: true})
		}
		// A kept segment between each pair.
		pieces = append(pieces, hls.Piece{Start: s + 6, End: s + 12, Segment: seg + 1})
	}
	// Renumber: the kept segments above overlap the next ones; drop them
	// but for the last.
	var plan []hls.Piece
	for i, p := range pieces {
		if !p.Edge && i != len(pieces)-1 {
			continue
		}
		plan = append(plan, p)
	}
	spans := audioSpans(plan, frame)
	edges := hls.Edges(plan)
	require.Equal(t, len(edges), len(spans))
	drift := 0.0
	for n, e := range edges {
		sp := spans[n]
		frames := (sp.End - sp.Start) / frame
		assert.InDelta(t, math.Round(frames), frames, 1e-6, "edge %d: whole frames", n)
		drift += (sp.End - sp.Start) - (e.End - e.Start)
		assert.True(t, math.Abs(drift) <= frame/2+1e-9, "edge %d: drift %.4f s", n, drift)
		if sp.Start != e.Start {
			assert.Equal(t, e.End, sp.End, "edge %d keeps one end", n)
		}
		assert.InDelta(t, e.Start, sp.Start, frame, "edge %d start", n)
		assert.InDelta(t, e.End, sp.End, frame, "edge %d end", n)
	}
	// An edge that ends where its segment does, before media that follows
	// without a cut, keeps its end; one before a cut keeps its start.
	for n, e := range edges {
		switch math.Mod(e.End, 6) {
		case 0:
			assert.Equal(t, e.End, spans[n].End, "edge %d end", n)
		default:
			assert.Equal(t, e.Start, spans[n].Start, "edge %d start", n)
		}
	}

	// The track's last piece ends where its index does, and nothing follows:
	// its span stops at or before that end (the source may decode no
	// further), whatever the drift before it.
	for _, length := range []float64{2.0, 2.01, 2.02, 2.0232} {
		last := []hls.Piece{
			{Start: 0, End: 1.71, Segment: 0, Edge: true},
			{Start: 4, End: 4 + length, Segment: 1, Edge: true},
		}
		spans := audioSpans(last, frame)
		require.Equal(t, 2, len(spans))
		sp := spans[1]
		assert.Equal(t, last[1].Start, sp.Start, "last edge of %.4f s keeps its start", length)
		assert.True(t, sp.End <= last[1].End+1e-9, "last edge of %.4f s ends at %.4f s, past %.4f s", length, sp.End, last[1].End)
		assert.True(t, sp.End > last[1].End-frame, "last edge of %.4f s ends at %.4f s", length, sp.End)
		frames := (sp.End - sp.Start) / frame
		assert.InDelta(t, math.Round(frames), frames, 1e-6, "last edge of %.4f s: whole frames", length)
	}
}
