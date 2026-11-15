package hls_test

import (
	"os"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func readCues(t *testing.T, name string) []hls.Cue {
	t.Helper()
	f, err := os.Open("../testdata/captions/" + name)
	require.NoError(t, err)
	t.Cleanup(func() { f.Close() })
	cues, err := hls.ParseVTT(f)
	require.NoError(t, err)
	return cues
}

func TestParseVTTYouTubeRolling(t *testing.T) {
	cues := readCues(t, "youtube-auto.vtt")
	texts := make([]string, len(cues))
	for i, c := range cues {
		texts[i] = c.Text
	}
	assert.Equal(t, []string{"hello world this", "is a test", "of captions"}, texts)
	assert.InDelta(t, 0.16, cues[0].Start, 0.0005)
	assert.InDelta(t, 2.27, cues[0].End, 0.0005)
	assert.InDelta(t, 2.28, cues[1].Start, 0.0005)
	// No word lost or doubled.
	assert.Equal(t, "hello world this is a test of captions", strings.Join(strings.Fields(strings.Join(texts, " ")), " "))
}

func TestParseVTTForms(t *testing.T) {
	cues := readCues(t, "whisper.vtt")
	assert.Equal(t, 2, len(cues))
	assert.Equal(t, "First line.", cues[0].Text)
	assert.InDelta(t, 1.0, cues[0].Start, 0.0005)
	assert.InDelta(t, 5.5, cues[1].End, 0.0005)
}

func TestParseVTTNotVTT(t *testing.T) {
	_, err := hls.ParseVTT(strings.NewReader("1\n00:00:01,000 --> 00:00:02,000\nSRT\n"))
	assert.True(t, err != nil, "an SRT file is not WebVTT")
}

// A two-line cue that is not rolling keeps both lines.
func TestParseVTTMultiLine(t *testing.T) {
	cues, err := hls.ParseVTT(strings.NewReader("WEBVTT\n\n00:00:01.000 --> 00:00:03.000\nTop\nBottom\n"))
	require.NoError(t, err)
	assert.Equal(t, "Top\nBottom", cues[0].Text)
}

// A malformed cue is skipped, the rest are kept.
func TestParseVTTSkipsMalformed(t *testing.T) {
	cues, err := hls.ParseVTT(strings.NewReader("WEBVTT\n\n00:00:xx.000 --> 00:00:03.000\nBad\n\n00:00:04.000 --> 00:00:05.000\nGood\n"))
	require.NoError(t, err)
	require.Equal(t, 1, len(cues))
	assert.Equal(t, "Good", cues[0].Text)
}
