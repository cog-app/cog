package hls_test

import (
	"fmt"
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

func TestVTTSegmentOverlap(t *testing.T) {
	cues := []hls.Cue{{Start: 1, End: 2, Text: "a"}, {Start: 5.5, End: 7, Text: "b"}, {Start: 8, End: 9, Text: "c"}}
	s0 := hls.VTTSegment(cues, 0, 6, 0)
	s1 := hls.VTTSegment(cues, 6, 12, 0)
	assert.True(t, strings.HasPrefix(s0, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n"), s0)
	assert.True(t, strings.Contains(s0, "00:00:01.000 --> 00:00:02.000\na\n"), s0)
	assert.True(t, strings.Contains(s0, "00:00:05.500 --> 00:00:07.000\nb\n"), s0) // crosses into s1, unclipped
	assert.True(t, strings.Contains(s1, "00:00:05.500 --> 00:00:07.000\nb\n"), s1)
	assert.False(t, strings.Contains(s0, "\nc\n"), s0)
}

func TestVTTSegmentEmpty(t *testing.T) {
	assert.Equal(t, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:0,LOCAL:00:00:00.000\n\n", hls.VTTSegment(nil, 0, 6, 0))
}

func TestVTTSegmentEscapesArrow(t *testing.T) {
	s := hls.VTTSegment([]hls.Cue{{Start: 1, End: 2, Text: "a --> b"}}, 0, 6, 0)
	assert.True(t, strings.Contains(s, "a --&gt; b\n"), s)
}

func TestVTTSegmentCarriesMPEGTS(t *testing.T) {
	assert.True(t, strings.Contains(hls.VTTSegment(nil, 0, 6, 135000), "X-TIMESTAMP-MAP=MPEGTS:135000,LOCAL:00:00:00.000"))
}

// Caption time 0 is the audio's first sample: its media time at 90 kHz.
func TestTimestampMap(t *testing.T) {
	at := func(start float64) hls.Track {
		return hls.Track{Kind: hls.Audio, Fragments: []hls.Fragment{{Start: start, Duration: 2}}}
	}
	assert.Equal(t, int64(135000), hls.TimestampMap(at(1.5)))
	assert.Equal(t, int64(0), hls.TimestampMap(at(0)))
	assert.Equal(t, int64(132910), hls.TimestampMap(at(1.4767800453514739)))
	assert.Equal(t, int64(0), hls.TimestampMap(hls.Track{}))
}

func TestSubtitleEntries(t *testing.T) {
	es := hls.SubtitleEntries(audioTrack(), func(n int) string { return fmt.Sprintf("subs-%d.vtt?sig=x", n) })
	assert.Equal(t, 2, len(es)) // the 4 × 2 s audio track makes segments of 6 s and 2 s
	assert.Equal(t, "subs-1.vtt?sig=x", es[1].URI)
	assert.InDelta(t, 2.0, es[1].Duration, 1e-9)
	assert.Equal(t, int64(0), es[0].Length)
	assert.Equal(t, "", es[0].Init.URI)
}

func TestSegmentCaptionBoundsOffsetStart(t *testing.T) {
	// The audio starts at 1.5 s of media time; caption time 0 is its first
	// sample, so segment 1 (media 7.5 s) covers captions 6 s to 12 s.
	var frags []hls.Fragment
	for i := range 4 {
		frags = append(frags, hls.Fragment{Start: 1.5 + 3*float64(i), Duration: 3, Offset: int64(i) * 100, Length: 100})
	}
	audio := hls.Track{Kind: hls.Audio, Fragments: frags}
	start, end, ok := hls.SegmentCaptionBounds(audio, 1)
	require.True(t, ok)
	assert.Equal(t, 6.0, start)
	assert.Equal(t, 12.0, end)
	_, _, ok = hls.SegmentCaptionBounds(audio, 2)
	assert.False(t, ok)
	_, _, ok = hls.SegmentCaptionBounds(audio, -1)
	assert.False(t, ok)

	cues := []hls.Cue{{Start: 5, End: 5.5, Text: "early"}, {Start: 7, End: 8, Text: "inside"}}
	seg := hls.VTTSegment(cues, start, end, 0)
	assert.True(t, strings.Contains(seg, "inside"), seg)
	assert.False(t, strings.Contains(seg, "early"), seg)
}

func parseTexts(t *testing.T, src string) []string {
	t.Helper()
	cues, err := hls.ParseVTT(strings.NewReader(src))
	require.NoError(t, err)
	texts := make([]string, len(cues))
	for i, c := range cues {
		texts[i] = c.Text
	}
	return texts
}

// A word that really was said twice is not folded away.
func TestParseVTTKeepsRepeatedCue(t *testing.T) {
	got := parseTexts(t, "WEBVTT\n\n00:01.000 --> 00:02.000\nNo.\n\n00:02.000 --> 00:03.000\nNo.\n")
	assert.Equal(t, []string{"No.", "No."}, got)
}

// Repeated leading lines are still folded while a line remains.
func TestParseVTTFoldsLeadingRepeat(t *testing.T) {
	got := parseTexts(t, "WEBVTT\n\n00:01.000 --> 00:02.000\nA\n\n00:02.000 --> 00:03.000\nA\nB\n")
	assert.Equal(t, []string{"A", "B"}, got)
}

func TestParseVTTStrictTimes(t *testing.T) {
	for _, ts := range []string{"00:NaN", "00:Inf", "00:0x1p3", "00:+1", "00:-1", "00:1e1", "00:1_0", "00:00:01.", "00:00:.5", "00:00:01.5x"} {
		got := parseTexts(t, "WEBVTT\n\n00:00:01.000 --> "+ts+"\nBad\n\n00:04.000 --> 00:05.000\nGood\n")
		assert.Equal(t, []string{"Good"}, got, ts)
	}
	got := parseTexts(t, "WEBVTT\n\n00:01.5 --> 01:00:02.250\nOK\n")
	assert.Equal(t, []string{"OK"}, got)
}

func TestParseVTTStrictHeader(t *testing.T) {
	for _, h := range []string{"WEBVTT", "WEBVTT - title", "WEBVTT\ttitle"} {
		_, err := hls.ParseVTT(strings.NewReader(h + "\n\n00:01.000 --> 00:02.000\nx\n"))
		assert.NoError(t, err, h)
	}
	_, err := hls.ParseVTT(strings.NewReader("WEBVTTX\n\n00:01.000 --> 00:02.000\nx\n"))
	assert.True(t, err != nil, "WEBVTTX is not WebVTT")
}

func TestParseVTTUnescapes(t *testing.T) {
	got := parseTexts(t, "WEBVTT\n\n00:01.000 --> 00:02.000\na &amp; b &lt;c&gt;&nbsp;d\n")
	assert.Equal(t, []string{"a & b <c> d"}, got)
}

func TestVTTSegmentEscapesText(t *testing.T) {
	s := hls.VTTSegment([]hls.Cue{{Start: 1, End: 2, Text: "x < 5 & y"}}, 0, 6, 0)
	assert.True(t, strings.Contains(s, "x &lt; 5 &amp; y\n"), s)
}

// Under cuts, a cue keeps its caption time but is clipped to the piece, so
// one that crosses a cut is not shown twice.
func TestVTTPieceClips(t *testing.T) {
	cues := []hls.Cue{{Start: 1, End: 3, Text: "A"}, {Start: 4, End: 6, Text: "B"}, {Start: 7, End: 8, Text: "C"}}
	got := hls.VTTPiece(cues, 2.5, 5, 135000)
	assert.Equal(t, "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:135000,LOCAL:00:00:00.000\n\n"+
		"00:00:02.500 --> 00:00:03.000\nA\n\n00:00:04.000 --> 00:00:05.000\nB\n\n", got)
}

func TestPieceSubtitleEntries(t *testing.T) {
	pieces := []hls.Piece{{Start: 0, End: 2.6, Edge: true}, {Start: 4.1, End: 6, Edge: true}, {Start: 6, End: 12, Segment: 1}}
	entries := hls.PieceSubtitleEntries(pieces, func(n int) string { return fmt.Sprintf("s%d", n) })
	require.Equal(t, 3, len(entries))
	for i, want := range []hls.Entry{
		{URI: "s0", Duration: 2.6},
		{URI: "s1", Duration: 1.9, Discontinuity: true},
		{URI: "s2", Duration: 6},
	} {
		assert.Equal(t, want.URI, entries[i].URI)
		assert.InDelta(t, want.Duration, entries[i].Duration, 1e-9)
		assert.Equal(t, want.Discontinuity, entries[i].Discontinuity, "entry %d", i)
	}
}
