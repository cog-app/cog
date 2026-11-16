package feed_test

import (
	"bytes"
	"encoding/json"
	"math"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// TestHLSSkipPlaysInFFmpeg reads a skip presentation as mpv does (ffmpeg's
// HLS demuxer) and checks it plays the item less its cuts: every source
// video frame outside the cuts once, in order and at its source's time, the
// audio as long, and nothing for ffmpeg to warn about. The stock video is
// x264 baseline like the edges; the high-profile one, with B-frames and a
// 90 kHz timescale, is a source the edge encoder does not match.
//
// ffmpeg keeps the first init it reads, so an edge must decode under a kept
// segment's init, and a kept segment under an edge's: each order is a case.
func TestHLSSkipPlaysInFFmpeg(t *testing.T) {
	needFFmpeg(t)
	// 18 s tracks, made as make.sh makes the fixtures, have three segments:
	// room for an edge between two kept ones.
	long := map[string]string{}
	dir := t.TempDir()
	for _, profile := range []string{"baseline", "high"} {
		video := filepath.Join(dir, profile+".mp4")
		args := []string{"-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=24:duration=18",
			"-c:v", "libx264", "-profile:v", profile, "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p"}
		if profile == "high" {
			args = append(args, "-video_track_timescale", "90000")
		}
		args = append(args, "-movflags", "+frag_keyframe+empty_moov+default_base_moof+global_sidx", "-f", "mp4", video)
		ffclient(t, "ffmpeg", args...)
		long[profile] = video
	}
	audio := filepath.Join(dir, "audio.m4a")
	ffclient(t, "ffmpeg", "-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=18", "-c:a", "aac", "-b:a", "32k",
		"-movflags", "+empty_moov+default_base_moof+global_sidx", "-frag_duration", "2000000", "-f", "mp4", audio)

	for _, c := range []struct {
		name    string
		long    bool // 18 s, three segments; else the 12 s fixtures, two
		silence []feed.Range
		want    float64 // s of audio
	}{
		// {2.6,4.1} and {7.1,9.5}: every piece is an edge.
		{"edges", false, skipFixture, 8.1},
		// {7.1,9.5}: [0,6) is kept, then edges.
		{"kept then edges", false, skipFixture[1:], 9.6},
		// {2.6,4.1}: edges, then [6,12) is kept.
		{"edges then kept", false, skipFixture[:1], 10.5},
		// {7.1,9.5}: [0,6) kept, edges, [12,18) kept.
		{"kept edges kept", true, skipFixture[1:], 15.6},
	} {
		for _, profile := range []string{"baseline", "high"} {
			t.Run(c.name+"/"+profile, func(t *testing.T) {
				audioSrc, videoSrc := "audio.m4a", map[string]string{"baseline": "video.mp4", "high": "video-high.mp4"}[profile]
				if c.long {
					audioSrc, videoSrc = audio, long[profile]
				}
				env := cogtest.New(t, feed.New())
				env.CreateUser(t, "alice", "pw")
				id := packagedItemFrom(t, env, "alice", audioSrc, videoSrc)
				store := feedStore(t, env, "alice")
				require.NoError(t, store.SetSilenceRanges(id, c.silence))
				skip := skipMaster(t, env, "alice", id)
				env.Get(t, skip).Status(http.StatusOK)
				runEdges(t, env, "alice")
				body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
				edit := masterEdit(body)
				require.NotEqual(t, "", edit, "edges not made: %s\n%s", download(t, env, "alice", id).EdgesError, body)
				cuts, _, err := store.EditCuts(id, edit)
				require.NoError(t, err)

				// The source's frames outside the cuts. A frame at a cut's
				// start is cut; one at its end is kept.
				if !filepath.IsAbs(videoSrc) {
					videoSrc = "testdata/media/" + videoSrc
				}
				var want []float64
				for _, f := range probeFrames(t, videoSrc).Frames {
					pts := f.pts(t)
					cut := false
					for _, c := range cuts {
						cut = cut || (pts >= c.Start-1e-6 && pts < c.End-1e-6)
					}
					if !cut {
						want = append(want, pts)
					}
				}

				// Over the env's own listener, so ffmpeg fetches the
				// playlists, inits and byte ranges itself, as mpv would. Both
				// streams are decoded: a stream ffmpeg discards reads short
				// at 32 KiB into a segment and is reported corrupt.
				probe := probeFrames(t, env.Providers.Config.BaseURL+skip, "-protocol_whitelist", "http,tcp,hls,file,crypto")
				var audioStreams []int
				rate := 0
				for _, s := range probe.Streams {
					if s.CodecType == "audio" {
						audioStreams = append(audioStreams, s.Index)
						r, err := strconv.Atoi(s.SampleRate)
						require.NoError(t, err, s.SampleRate)
						rate = r
					}
				}
				require.Equal(t, 1, len(audioStreams), "audio streams")
				var samples int
				var got []float64
				for _, f := range probe.Frames {
					switch {
					case f.MediaType == "audio" && f.StreamIndex == audioStreams[0]:
						samples += f.Samples
					case f.MediaType == "video":
						got = append(got, f.pts(t))
					}
				}
				audioSecs := float64(samples) / float64(rate)
				t.Logf("audio %d samples at %d Hz = %.4f s; video %d frames, want %d", samples, rate, audioSecs, len(got), len(want))
				assert.InDelta(t, c.want, audioSecs, 0.1)
				assert.Equal(t, len(want), len(got), "video frames")
				for i := range min(len(want), len(got)) {
					if d := got[i] - want[i]; d < -1e-3 || d > 1e-3 {
						t.Errorf("frame %d at %.4f s, want %.4f s (the source's)", i, got[i], want[i])
						break
					}
				}
			})
		}
	}
}

// TestHLSSkipManyCutsInSync plays a minute of video with 22 cuts as mpv
// does and checks the audio stays with the video: a player that keeps time
// by the audio it has played (mpv) drifts by however much each audio edge
// is longer or shorter than its piece, and an hour has hundreds of edges.
// The cuts are on video frames, so the video plays exactly the presentation
// and the audio is what is measured.
func TestHLSSkipManyCutsInSync(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	video, audio := filepath.Join(dir, "video.mp4"), filepath.Join(dir, "audio.m4a")
	ffclient(t, "ffmpeg", "-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=24:duration=60",
		"-c:v", "libx264", "-profile:v", "baseline", "-g", "48", "-keyint_min", "48", "-sc_threshold", "0", "-pix_fmt", "yuv420p",
		"-movflags", "+frag_keyframe+empty_moov+default_base_moof+global_sidx", "-f", "mp4", video)
	ffclient(t, "ffmpeg", "-nostdin", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=60",
		"-c:a", "aac", "-b:a", "32k", "-movflags", "+empty_moov+default_base_moof+global_sidx", "-frag_duration", "2000000", "-f", "mp4", audio)

	// Cuts of 21 to 27 frames, 1.5 s or more apart, on frames; a silence
	// keeps CutMargin at each end of its cut.
	var silence []feed.Range
	for i := range 22 {
		start := math.Round((1.3+float64(i)*2.65)*24) / 24
		end := start + float64(21+3*(i%3))/24
		silence = append(silence, feed.Range{Start: start - hls.CutMargin, End: end + hls.CutMargin, Action: "skip"})
	}
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItemFrom(t, env, "alice", audio, video)
	store := feedStore(t, env, "alice")
	require.NoError(t, store.SetSilenceRanges(id, silence))
	skip := skipMaster(t, env, "alice", id)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	for range 5 {
		runEdges(t, env, "alice")
		if body = env.Get(t, skip).Status(http.StatusOK).ReadBody(); masterEdit(body) != "" {
			break
		}
	}
	edit := masterEdit(body)
	require.NotEqual(t, "", edit, "edges not made: %s\n%s", download(t, env, "alice", id).EdgesError, body)
	cuts, duration, err := store.EditCuts(id, edit)
	require.NoError(t, err)
	require.Equal(t, 22, len(cuts))
	presentation := duration
	for _, c := range cuts {
		presentation -= c.End - c.Start
	}

	probe := probeFrames(t, env.Providers.Config.BaseURL+skip, "-protocol_whitelist", "http,tcp,hls,file,crypto")
	rate, samples, frames := 0, 0, 0
	for _, s := range probe.Streams {
		if s.CodecType == "audio" {
			r, err := strconv.Atoi(s.SampleRate)
			require.NoError(t, err, s.SampleRate)
			rate = r
		}
	}
	require.NotEqual(t, 0, rate, "no audio stream")
	for _, f := range probe.Frames {
		switch f.MediaType {
		case "audio":
			samples += f.Samples
		case "video":
			frames++
		}
	}
	audioSecs, videoSecs := float64(samples)/float64(rate), float64(frames)/24
	t.Logf("audio %.4f s, video %.4f s, presentation %.4f s", audioSecs, videoSecs, presentation)
	assert.InDelta(t, presentation, videoSecs, 1.0/24, "video")
	assert.InDelta(t, videoSecs, audioSecs, 0.05, "audio against video")
	assert.InDelta(t, presentation, audioSecs, 0.05, "audio against the presentation")
}

type frameProbe struct {
	Frames  []probedFrame `json:"frames"`
	Streams []struct {
		Index      int    `json:"index"`
		CodecType  string `json:"codec_type"`
		SampleRate string `json:"sample_rate"`
	} `json:"streams"`
}

type probedFrame struct {
	MediaType   string `json:"media_type"`
	StreamIndex int    `json:"stream_index"`
	PTS         string `json:"pts_time"`
	Samples     int    `json:"nb_samples"`
}

func (f probedFrame) pts(t *testing.T) float64 {
	t.Helper()
	v, err := strconv.ParseFloat(f.PTS, 64)
	require.NoError(t, err, f.PTS)
	return v
}

// probeFrames decodes every stream of in with ffprobe.
func probeFrames(t *testing.T, in string, opts ...string) frameProbe {
	t.Helper()
	args := append([]string{"-v", "warning"}, opts...)
	args = append(args, "-show_entries", "frame=media_type,stream_index,pts_time,nb_samples:stream=index,codec_type,sample_rate", "-of", "json", in)
	var p frameProbe
	require.NoError(t, json.Unmarshal(ffclient(t, "ffprobe", args...), &p))
	return p
}

// duplicateMOOV is how ffmpeg's mov demuxer says it skipped the init of an
// EXT-X-MAP after the first: the HLS demuxer keeps the first moov it reads,
// which is what edges are made to decode under (pipeline.Edges).
const duplicateMOOV = "Found duplicated MOOV Atom. Skipped it"

// ffclient runs ffmpeg or ffprobe as the test's client, standing in for
// mpv, and returns its stdout. It fails the test on any line on stderr but
// duplicateMOOV: at -v warning that is a decode error, a corrupt packet or
// a timestamp ffmpeg had to fix.
func ffclient(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, "%s: %s", name, stderr.String())
	for line := range strings.SplitSeq(strings.TrimSpace(stderr.String()), "\n") {
		// ffmpeg folds repeats of a line into "Last message repeated n times".
		if line == "" || strings.HasSuffix(line, duplicateMOOV) || strings.HasPrefix(strings.TrimSpace(line), "Last message repeated") {
			continue
		}
		t.Errorf("%s: %s", name, line)
	}
	return out
}
