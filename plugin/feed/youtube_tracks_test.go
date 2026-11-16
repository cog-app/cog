package feed

import (
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
