package feed_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestHLSSkipPlaysInFFmpeg reads a skip presentation as mpv does (ffmpeg's
// HLS demuxer) and checks its audio and video run as long as the item less
// its cuts, every video frame once, in order and at its source's time, with
// nothing for ffmpeg to warn about. The stock video is x264 baseline like the edges; the
// high-profile one, with B-frames and a 90 kHz timescale, is a source the
// edge encoder does not match.
func TestHLSSkipPlaysInFFmpeg(t *testing.T) {
	for _, c := range []struct {
		name    string
		silence []feed.Range
		cuts    int
		want    float64 // s
	}{
		// {2.6,4.1} and {7.1,9.5}: every piece is an edge.
		{"edges", skipFixture, 2, 8.1},
		// {7.1,9.5}: the first segment is kept, so its init is the one
		// ffmpeg keeps, and the edges after it decode under it.
		{"kept first", skipFixture[1:], 1, 9.6},
	} {
		for _, fixture := range []string{"video.mp4", "video-high.mp4"} {
			t.Run(c.name+"/"+fixture, func(t *testing.T) {
				needFFmpeg(t)
				env := cogtest.New(t, feed.New())
				env.CreateUser(t, "alice", "pw")
				id := packagedItemFrom(t, env, "alice", "audio.m4a", fixture)
				require.NoError(t, feedStore(t, env, "alice").SetSilenceRanges(id, c.silence))
				skip := skipMaster(t, env, "alice", id)
				env.Get(t, skip).Status(http.StatusOK)
				runEdges(t, env, "alice")
				body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
				require.NotEqual(t, "", masterEdit(body), "edges not made: %s\n%s", download(t, env, "alice", id).EdgesError, body)

				// Over the env's own listener, so ffmpeg fetches the
				// playlists, inits and byte ranges itself, as mpv would. Both
				// streams are decoded: a stream ffmpeg discards reads short
				// at 32 KiB into a segment and is reported corrupt.
				out := ffclient(t, "ffprobe", "-v", "warning", "-protocol_whitelist", "http,tcp,hls,file,crypto",
					"-show_entries", "frame=media_type,pts_time,nb_samples:stream=codec_type,sample_rate", "-of", "json",
					env.Providers.Config.BaseURL+skip)
				var probe struct {
					Frames []struct {
						MediaType string `json:"media_type"`
						PTS       string `json:"pts_time"`
						Samples   int    `json:"nb_samples"`
					} `json:"frames"`
					Streams []struct {
						CodecType  string `json:"codec_type"`
						SampleRate string `json:"sample_rate"`
					} `json:"streams"`
				}
				require.NoError(t, json.Unmarshal(out, &probe))
				rate := 0
				for _, s := range probe.Streams {
					if s.CodecType == "audio" {
						r, err := strconv.Atoi(s.SampleRate)
						require.NoError(t, err, s.SampleRate)
						rate = r
					}
				}
				require.True(t, rate > 0, "no audio stream: %s", out)
				// The source's first frame: the B-frame delay puts the
				// high-profile one at 2/24 s.
				first := ffclient(t, "ffprobe", "-v", "warning", "-select_streams", "v:0", "-read_intervals", "%+#1",
					"-show_entries", "frame=pts_time", "-of", "csv=p=0", "testdata/media/"+fixture)
				firstPTS, err := strconv.ParseFloat(strings.Trim(string(first), ",\n"), 64)
				require.NoError(t, err, string(first))

				var samples, frames, gaps int
				last := -1.0
				for _, f := range probe.Frames {
					switch f.MediaType {
					case "audio":
						samples += f.Samples
					case "video":
						pts, err := strconv.ParseFloat(f.PTS, 64)
						require.NoError(t, err, f.PTS)
						if frames == 0 {
							assert.InDelta(t, firstPTS, pts, 1e-3, "first frame")
						} else {
							assert.True(t, pts > last, "frame %d at %.4f s after %.4f s", frames, pts, last)
						}
						// A frame further than 1.5 frames from the last is a cut
						// or a frame lost at a joint.
						if frames > 0 && pts-last > 1.5/24 {
							gaps++
							t.Logf("frame %d at %.4f s after %.4f s", frames, pts, last)
						}
						last = pts
						frames++
					}
				}
				audioSecs := float64(samples) / float64(rate)
				videoSecs := float64(frames) / 24
				assert.Equal(t, c.cuts, gaps, "gaps between frames")
				t.Logf("audio %d samples at %d Hz = %.4f s; video %d frames = %.4f s", samples, rate, audioSecs, frames, videoSecs)
				assert.InDelta(t, c.want, audioSecs, 0.1)
				assert.InDelta(t, c.want, videoSecs, 0.1)
			})
		}
	}
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
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		// ffmpeg folds repeats of a line into "Last message repeated n times".
		if line == "" || strings.HasSuffix(line, duplicateMOOV) || strings.HasPrefix(strings.TrimSpace(line), "Last message repeated") {
			continue
		}
		t.Errorf("%s: %s", name, line)
	}
	return out
}
