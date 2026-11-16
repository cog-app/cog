package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func TestX264Profile(t *testing.T) {
	for codecs, want := range map[string][2]string{
		"avc1.42C01E": {"baseline", "3.0"},
		"avc1.4D401F": {"main", "3.1"},
		"avc1.640028": {"high", "4.0"},
		"avc3.640033": {"high", "5.1"},
	} {
		p, l, ok := feed.X264Profile(codecs)
		assert.True(t, ok, codecs)
		assert.Equal(t, want[0], p, codecs)
		assert.Equal(t, want[1], l, codecs)
	}
	for _, codecs := range []string{"vp09.00.40.08", "", "avc1.6E0028", "avc1.7A0028", "avc1.F40028", "avc1.64", "avc1.zz0028"} {
		_, _, ok := feed.X264Profile(codecs)
		assert.False(t, ok, codecs)
	}
}

// twoSegments is a 12 s track of two 6 s segments.
func twoSegments(kind hls.Kind, codecs string) hls.Track {
	return hls.Track{Kind: kind, Codecs: codecs, InitLength: 100, Fragments: []hls.Fragment{
		{Start: 0, Duration: 6, Offset: 100, Length: 1000, Keyframe: true},
		{Start: 6, Duration: 6, Offset: 1100, Length: 1000, Keyframe: true},
	}}
}

// Edges are made only where they decode under the source's init: AAC-LC
// audio, and H.264 that x264 can match. Anything else leaves the whole
// presentation unskipped, so audio and video never disagree.
func TestSkipPlanSupport(t *testing.T) {
	store, id := readyItem(t)
	d := feed.Download{ItemID: id, SilenceRanges: `[{"start":2.5,"end":4.2,"action":"skip"}]`}
	lc, he := twoSegments(hls.Audio, "mp4a.40.2"), twoSegments(hls.Audio, "mp4a.40.5")
	for name, c := range map[string]struct {
		original     hls.Track
		clean, video *hls.Track
		unsupported  bool
	}{
		"aac-lc":         {original: lc},
		"he-aac":         {original: he, unsupported: true},
		"he-aac clean":   {original: lc, clean: &he, unsupported: true},
		"baseline video": {original: lc, video: ptr(twoSegments(hls.Video, "avc1.42C01E"))},
		"high 10 video":  {original: lc, video: ptr(twoSegments(hls.Video, "avc1.6E0028")), unsupported: true},
		"vp9 video":      {original: lc, video: ptr(twoSegments(hls.Video, "")), unsupported: true},
	} {
		p, err := feed.SkipPlan(store, d, c.original, c.clean, c.video)
		require.NoError(t, err)
		assert.Equal(t, c.unsupported, p.Unsupported, name)
		assert.Equal(t, 1, len(p.Cuts), name)
		assert.NotEqual(t, "", p.Edit, name)
		if c.unsupported {
			assert.Equal(t, 0, len(p.Original)+len(p.Clean)+len(p.Video), "%s: pieces of an unsupported plan", name)
		} else {
			// {2.6,4.1} inside [0,6): edges [0,2.6) and [4.1,6).
			assert.Equal(t, 2, len(hls.Edges(p.Original)), name)
		}
	}
}

func ptr[T any](v T) *T { return &v }
