package pipeline

import (
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
	src := edgeSource{Kind: hls.Audio, Codecs: "mp4a.40.2", SampleRate: 44100, Channels: 2}
	args, err := edgeArgs("/in.m4a", "/out.mp4", hls.Piece{Start: 2.6, End: 6, Edge: true}, src)
	require.NoError(t, err)
	for _, p := range [][2]string{
		{"-ss", "2.600000"}, {"-to", "6.000000"}, {"-output_ts_offset", "2.600000"},
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
	src := edgeSource{Kind: hls.Audio, T0: 1.5, SampleRate: 44100, Channels: 1}
	args, err := edgeArgs("/in.m4a", "/out.mp4", hls.Piece{Start: 4.1, End: 6, Edge: true}, src)
	require.NoError(t, err)
	assert.True(t, pair(args, "-ss", "2.600000"), "%q", args)
	assert.True(t, pair(args, "-to", "4.500000"), "%q", args)
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
