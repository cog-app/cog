package hls_test

import (
	"fmt"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

// synthetic is 9 × 2 s audio fragments, so its segments are [0,6) [6,12) [12,18).
func synthetic() hls.Track {
	return gridTrack(2, 9)
}

// gridTrack is n audio fragments of d seconds at offsets 1000+100·i, length 100.
func gridTrack(d float64, n int) hls.Track {
	tr := hls.Track{Kind: hls.Audio, Codecs: "mp4a.40.2", InitLength: 1000}
	for i := range n {
		tr.Fragments = append(tr.Fragments, hls.Fragment{
			Start: float64(i) * d, Duration: d, Offset: 1000 + 100*int64(i), Length: 100, Keyframe: i == 0,
		})
	}
	return tr
}

// edgeTracks is n edge files, each an init of 500 bytes and one fragment.
func edgeTracks(n int) []hls.Track {
	out := make([]hls.Track, n)
	for i := range out {
		out[i] = hls.Track{Kind: hls.Audio, InitLength: 500, Fragments: []hls.Fragment{{Duration: 1, Offset: 500, Length: 50, Keyframe: true}}}
	}
	return out
}

func assertPieces(t *testing.T, want, got []hls.Piece) {
	t.Helper()
	require.Equal(t, len(want), len(got), "pieces: %v", got)
	for i := range want {
		assert.InDelta(t, want[i].Start, got[i].Start, 1e-9, "piece %d start: %v", i, got)
		assert.InDelta(t, want[i].End, got[i].End, 1e-9, "piece %d end: %v", i, got)
		assert.Equal(t, want[i].Segment, got[i].Segment, "piece %d segment: %v", i, got)
		assert.Equal(t, want[i].Edge, got[i].Edge, "piece %d edge: %v", i, got)
	}
}

func edgeName(n int) string { return fmt.Sprint("e", n) }

func TestPlanCutsInsideAndAcross(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{5, 13}})
	assertPieces(t, []hls.Piece{{0, 5, 0, true}, {13, 18, 2, true}}, got)
}

func TestPlanCutsOnBoundaries(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{6, 12}})
	assertPieces(t, []hls.Piece{{0, 6, 0, false}, {12, 18, 2, false}}, got)
}

func TestPlanCutsTwoInOneSegment(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{1, 2}, {3, 4}})
	assertPieces(t, []hls.Piece{{0, 1, 0, true}, {2, 3, 0, true}, {4, 6, 0, true}, {6, 12, 1, false}, {12, 18, 2, false}}, got)
}

func TestPlanCutsAtEnds(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{0, 2}, {16, 18}})
	assertPieces(t, []hls.Piece{{2, 6, 0, true}, {6, 12, 1, false}, {12, 16, 2, true}}, got)
	e := hls.CutEntries(synthetic(), got, "audio.m4a", edgeName, edgeTracks(2))
	assert.False(t, e[0].Discontinuity, "no discontinuity before the first entry")
	for i, x := range e {
		assert.False(t, x.Discontinuity, "entry %d: the cuts only trim the ends", i)
		assert.True(t, x.Duration > 0, "entry %d has a duration", i)
	}
	// Unsorted and overlapping cuts plan the same as their union.
	assertPieces(t, got, hls.PlanCuts(synthetic(), []hls.Cut{{16, 18}, {0, 1}, {0.5, 2}, {17, 20}}))
}

// A cut over the whole item keeps nothing: no pieces, no entries, and an
// empty (ENDLIST only) playlist. The route must not offer such a
// presentation; it serves the item unskipped instead.
func TestPlanCutsWholeItem(t *testing.T) {
	tr := synthetic()
	cuts := []hls.Cut{{0, 18}}
	assert.InDelta(t, 0, hls.PresentationDuration(cuts, 18), 1e-9)
	pieces := hls.PlanCuts(tr, cuts)
	assert.Equal(t, 0, len(pieces))
	assert.Equal(t, 0, len(hls.Edges(pieces)))
	e := hls.CutEntries(tr, pieces, "audio.m4a", edgeName, nil)
	assert.Equal(t, 0, len(e))
	pl := hls.Playlist(e)
	assert.Contains(t, pl, "#EXT-X-ENDLIST\n")
	assert.NotContains(t, pl, "#EXTINF")
	assertCuts(t, []hls.Cut{{0, 18}}, hls.CoarseCuts(tr, cuts))
	assertPieces(t, nil, hls.PlanCuts(tr, hls.CoarseCuts(tr, cuts)))
}

// Media less than 1 ms long is not kept.
func TestPlanCutsDropsSlivers(t *testing.T) {
	got := hls.PlanCuts(synthetic(), []hls.Cut{{6.0005, 12}})
	assertPieces(t, []hls.Piece{{0, 6, 0, false}, {12, 18, 2, false}}, got)
	got = hls.PlanCuts(synthetic(), []hls.Cut{{5.9995, 12}})
	assertPieces(t, []hls.Piece{{0, 6, 0, false}, {12, 18, 2, false}}, got)
}

// A piece shorter than MinPiece joins its neighbour in the same kept span,
// so no edge too short to hold a frame is encoded.
func TestPlanCutsJoinsShortPieces(t *testing.T) {
	// The cut ends 50 ms before a segment boundary: [11.95,12) joins [12,18).
	got := hls.PlanCuts(synthetic(), []hls.Cut{{5, 11.95}})
	assertPieces(t, []hls.Piece{{0, 5, 0, true}, {11.95, 18, 1, true}}, got)
	// The cut starts 50 ms after one: [6,6.05) joins [0,6).
	got = hls.PlanCuts(synthetic(), []hls.Cut{{6.05, 13}})
	assertPieces(t, []hls.Piece{{0, 6.05, 0, true}, {13, 18, 2, true}}, got)
	// Never across a cut: [6,6.05) is alone in its span between two cuts.
	got = hls.PlanCuts(synthetic(), []hls.Cut{{1, 6}, {6.05, 13}})
	assertPieces(t, []hls.Piece{{0, 1, 0, true}, {6, 6.05, 1, true}, {13, 18, 2, true}}, got)
}

func TestCoarseCuts(t *testing.T) {
	assertCuts(t, []hls.Cut{{6, 12}}, hls.CoarseCuts(synthetic(), []hls.Cut{{5, 13}}))
	assertCuts(t, []hls.Cut{{0, 12}}, hls.CoarseCuts(synthetic(), []hls.Cut{{0, 4}, {4, 12.5}}))
	assertCuts(t, nil, hls.CoarseCuts(synthetic(), []hls.Cut{{1, 5}}))
	assertCuts(t, []hls.Cut{{0, 6}, {12, 18}}, hls.CoarseCuts(synthetic(), []hls.Cut{{0, 7}, {11, 18}}))
	// A coarse plan has no edges.
	tr := synthetic()
	assert.Equal(t, 0, len(hls.Edges(hls.PlanCuts(tr, hls.CoarseCuts(tr, []hls.Cut{{5, 13}, {14, 17}})))))
}

func TestCutEntriesDiscontinuities(t *testing.T) {
	tr := synthetic()
	pieces := hls.PlanCuts(tr, []hls.Cut{{5, 13}})
	e := hls.CutEntries(tr, pieces, "audio.m4a", func(n int) string { return fmt.Sprint("edge-", n) }, edgeTracks(2))
	assert.Equal(t, 2, len(e))
	assert.Equal(t, "edge-0", e[0].URI)
	assert.Equal(t, "edge-0", e[0].Init.URI)
	assert.Equal(t, hls.Init{URI: "edge-0", Offset: 0, Length: 500}, e[0].Init)
	assert.Equal(t, int64(500), e[0].Offset)
	assert.Equal(t, int64(50), e[0].Length)
	assert.InDelta(t, 5.0, e[0].Duration, 1e-9)
	assert.False(t, e[0].Discontinuity)
	assert.Equal(t, "edge-1", e[1].URI)
	assert.True(t, e[1].Discontinuity)
	// A whole segment kept between two others needs no discontinuity.
	e = hls.CutEntries(tr, hls.PlanCuts(tr, nil), "audio.m4a", nil, nil)
	for _, x := range e {
		assert.False(t, x.Discontinuity)
	}
	assert.Equal(t, hls.MediaPlaylist(tr, "audio.m4a"), hls.Playlist(e))
}

// A file change inside a kept span changes the EXT-X-MAP, not the continuity.
func TestCutEntriesFileChangeIsNoDiscontinuity(t *testing.T) {
	tr := synthetic()
	pieces := hls.PlanCuts(tr, []hls.Cut{{5, 8}})
	// [0,5) edge, [8,12) edge, [12,18) whole.
	e := hls.CutEntries(tr, pieces, "audio.m4a", edgeName, edgeTracks(2))
	require.Equal(t, 3, len(e))
	assert.Equal(t, []bool{false, true, false}, []bool{e[0].Discontinuity, e[1].Discontinuity, e[2].Discontinuity})
	assert.Equal(t, "audio.m4a", e[2].URI)
	assert.Equal(t, hls.Init{URI: "audio.m4a", Length: 1000}, e[2].Init)
	pl := hls.Playlist(e)
	assert.Equal(t, 1, strings.Count(pl, "#EXT-X-DISCONTINUITY"))
	assert.Equal(t, 3, strings.Count(pl, "#EXT-X-MAP"))
}

// An edge of several fragments is one byte range from its first fragment to
// the end of its last.
func TestCutEntriesEdgeOfSeveralFragments(t *testing.T) {
	tr := synthetic()
	edge := hls.Track{Kind: hls.Audio, InitOffset: 0, InitLength: 600, Fragments: []hls.Fragment{
		{Start: 13, Duration: 2, Offset: 600, Length: 70},
		{Start: 15, Duration: 2, Offset: 670, Length: 80},
		{Start: 17, Duration: 1, Offset: 750, Length: 30},
	}}
	pieces := hls.PlanCuts(tr, []hls.Cut{{6, 13}})
	e := hls.CutEntries(tr, pieces, "audio.m4a", edgeName, []hls.Track{edge})
	require.Equal(t, 2, len(e))
	assert.Equal(t, hls.Entry{
		URI: "e0", Offset: 600, Length: 180, Duration: 5, Init: hls.Init{URI: "e0", Length: 600}, Discontinuity: true,
	}, e[1])
}

// discontinuities is where a playlist's discontinuities fall, in
// presentation time.
func discontinuities(entries []hls.Entry) []float64 {
	var at []float64
	pos := 0.0
	for _, e := range entries {
		if e.Discontinuity {
			at = append(at, pos)
		}
		pos += e.Duration
	}
	return at
}

// Renditions with different segment grids get the same discontinuities at
// the same presentation times, so a player lines them up by continuity.
func TestCutEntriesAlignAcrossGrids(t *testing.T) {
	cuts := []hls.Cut{{1, 2}, {5, 13}, {16.5, 17}}
	six := synthetic()       // segments [0,6) [6,12) [12,18)
	odd := gridTrack(2.5, 7) // with the 0.5 s below: segments [0,7.5) [7.5,15) [15,18)
	odd.Fragments = append(odd.Fragments, hls.Fragment{Start: 17.5, Duration: 0.5, Offset: 1700, Length: 100})
	entries := func(tr hls.Track) []hls.Entry {
		p := hls.PlanCuts(tr, cuts)
		return hls.CutEntries(tr, p, "a.m4a", edgeName, edgeTracks(len(hls.Edges(p))))
	}
	a, b := entries(six), entries(odd)
	assert.Equal(t, discontinuities(a), discontinuities(b))
	assert.Equal(t, 3, len(discontinuities(a)))
	total := func(e []hls.Entry) (d float64) {
		for _, x := range e {
			d += x.Duration
		}
		return d
	}
	assert.InDelta(t, hls.PresentationDuration(cuts, 18), total(a), 1e-9)
	assert.InDelta(t, hls.PresentationDuration(cuts, 18), total(b), 1e-9)
}

func TestEdges(t *testing.T) {
	pieces := hls.PlanCuts(synthetic(), []hls.Cut{{1, 2}, {6, 12}, {13, 14}})
	assertPieces(t, []hls.Piece{{0, 1, 0, true}, {2, 6, 0, true}, {12, 13, 2, true}, {14, 18, 2, true}}, hls.Edges(pieces))
	assertPieces(t, nil, hls.Edges(hls.PlanCuts(synthetic(), []hls.Cut{{6, 12}})))
}

func TestEditID(t *testing.T) {
	layouts := func(tr hls.Track) map[string]hls.Track { return map[string]hls.Track{"a": tr} }
	a := hls.EditID([]hls.Cut{{5, 13}}, layouts(synthetic()))
	assert.Equal(t, 12, len(a))
	assert.Equal(t, a, hls.EditID([]hls.Cut{{5, 13}}, layouts(synthetic())))
	assert.Equal(t, a, hls.EditID([]hls.Cut{{5.0001, 13}}, layouts(synthetic())), "cuts are rounded to the millisecond")
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13.5}}, layouts(synthetic())))
	other := synthetic()
	other.Fragments = other.Fragments[:8]
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13}}, layouts(other)))
	resized := synthetic()
	resized.Fragments[4].Length++
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13}}, layouts(resized)), "a re-encode changes the id")
	// The same layout as a different rendition, or with another rendition beside it, is another presentation.
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13}}, map[string]hls.Track{"c": synthetic()}))
	assert.NotEqual(t, a, hls.EditID([]hls.Cut{{5, 13}}, map[string]hls.Track{"a": synthetic(), "c": synthetic()}))
	for _, c := range a {
		assert.True(t, strings.ContainsRune("0123456789abcdef", c), "hex: %s", a)
	}
}
