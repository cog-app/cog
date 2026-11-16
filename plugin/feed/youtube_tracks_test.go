package feed

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func requestedFormatsFixture(t *testing.T) string {
	t.Helper()
	out, err := os.ReadFile("testdata/ytdlp-requested-formats.json")
	require.NoError(t, err)
	return string(out)
}

func TestParseRequestedFormats(t *testing.T) {
	video, audio, err := parseRequestedFormats([]byte(requestedFormatsFixture(t)))
	require.NoError(t, err)

	assert.Equal(t, video.FormatID, "133")
	assert.Equal(t, video.Kind, hls.Video)
	assert.True(t, strings.HasPrefix(video.URL, "https://rr"), video.URL)
	assert.Equal(t, audio.FormatID, "140")
	assert.Equal(t, audio.Kind, hls.Audio)
	assert.True(t, strings.HasPrefix(audio.URL, "https://rr"), audio.URL)
	want := time.Unix(1794826202, 0)
	assert.True(t, video.ExpiresAt.Equal(want), video.ExpiresAt.String())
	assert.True(t, audio.ExpiresAt.Equal(want), audio.ExpiresAt.String())
	assert.True(t, video.AvailableAt.Equal(time.Unix(1794804607, 0)), video.AvailableAt.String())
	assert.True(t, audio.AvailableAt.Equal(time.Unix(1794804608, 0)), audio.AvailableAt.String())
}

// A format with no available_at may be asked for at once.
func TestParseRequestedFormatsNoAvailableAt(t *testing.T) {
	in := strings.ReplaceAll(requestedFormatsFixture(t), `"available_at"`, `"other"`)
	video, audio, err := parseRequestedFormats([]byte(in))
	require.NoError(t, err)
	assert.True(t, video.AvailableAt.IsZero(), video.AvailableAt.String())
	assert.True(t, audio.AvailableAt.IsZero(), audio.AvailableAt.String())
}

func TestParseRequestedFormatsRejectsManifest(t *testing.T) {
	in := strings.Replace(requestedFormatsFixture(t), `"protocol": "https"`, `"protocol": "http_dash_segments"`, 1)
	_, _, err := parseRequestedFormats([]byte(in))
	require.Error(t, err)
}

func TestParseRequestedFormatsNeedsBoth(t *testing.T) {
	_, _, err := parseRequestedFormats([]byte("NA\n"))
	require.Error(t, err)

	one := `[{"format_id":"18","protocol":"https","vcodec":"avc1","acodec":"mp4a","url":"https://rr1.googlevideo.com/x?expire=1"}]`
	_, _, err = parseRequestedFormats([]byte(one))
	require.Error(t, err)
}

func TestParseRequestedFormatsChecksCodecs(t *testing.T) {
	// The video entry carries sound, so it is not a video-only track.
	in := strings.Replace(requestedFormatsFixture(t), `"acodec": "none"`, `"acodec": "mp4a.40.2"`, 1)
	_, _, err := parseRequestedFormats([]byte(in))
	require.Error(t, err)
}

// sleeps replaces the wait with one that records each duration and returns
// err.
func sleeps(t *testing.T, err error) *[]time.Duration {
	t.Helper()
	var got []time.Duration
	t.Cleanup(SetAvailableSleep(func(ctx context.Context, d time.Duration) error {
		got = append(got, d)
		return err
	}))
	return &got
}

func TestAwaitAvailableWaitsForTheLatest(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := sleeps(t, nil)
	v := YouTubeTrack{FormatID: "137", AvailableAt: now.Add(3 * time.Second)}
	a := YouTubeTrack{FormatID: "140", AvailableAt: now.Add(5 * time.Second)}
	require.NoError(t, AwaitAvailable(t.Context(), now, v, a))
	assert.Equal(t, []time.Duration{5 * time.Second}, *got)
}

// A URL already available, or with no available_at, is not waited for.
func TestAwaitAvailableNoWait(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := sleeps(t, nil)
	require.NoError(t, AwaitAvailable(t.Context(), now,
		YouTubeTrack{FormatID: "137"},
		YouTubeTrack{FormatID: "140", AvailableAt: now},
		YouTubeTrack{FormatID: "141", AvailableAt: now.Add(-time.Minute)}))
	assert.Equal(t, 0, len(*got))
}

// A URL available too far ahead is a failure now, not a long wait.
func TestAwaitAvailableTooFar(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	got := sleeps(t, nil)
	err := AwaitAvailable(t.Context(), now, YouTubeTrack{FormatID: "137", AvailableAt: now.Add(youtubeAvailableWait + time.Second)})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "137"), err.Error())
	assert.Equal(t, 0, len(*got))
}

func TestAwaitAvailableSleepFails(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	sleeps(t, context.Canceled)
	err := AwaitAvailable(t.Context(), now, YouTubeTrack{FormatID: "137", AvailableAt: now.Add(time.Second)})
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
}

// The real wait ends when its context does: a client that leaves is not
// waited for.
func TestAvailableSleepCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start := time.Now()
	err := sleepFor(ctx, time.Minute)
	assert.True(t, errors.Is(err, context.Canceled), "%v", err)
	assert.True(t, time.Since(start) < time.Second, "waited %s", time.Since(start))
	require.NoError(t, sleepFor(t.Context(), time.Millisecond))
}
