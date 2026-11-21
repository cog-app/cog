package hls_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed/hls"
)

func assertCuts(t *testing.T, want, got []hls.Cut) {
	t.Helper()
	require.Equal(t, len(want), len(got), "cuts: %v", got)
	for i := range want {
		assert.InDelta(t, want[i].Start, got[i].Start, 1e-9, "cut %d start", i)
		assert.InDelta(t, want[i].End, got[i].End, 1e-9, "cut %d end", i)
	}
}

func TestEffectiveCuts(t *testing.T) {
	got := hls.EffectiveCuts([]hls.Cut{{2.5, 4.2}, {5.0, 5.6}, {7.0, 9.6}, {9.9, 11.5}}, 1.0, 12)
	// 5.0–5.6 is under 1 s: dropped. 9.6→9.9 is a gap under 1 s: merged.
	assertCuts(t, []hls.Cut{{2.6, 4.1}, {7.1, 11.4}}, got)
}

func TestEffectiveCutsDropsBeforeMerging(t *testing.T) {
	// Two short silences close together are not merged into a long one.
	assertCuts(t, nil, hls.EffectiveCuts([]hls.Cut{{1, 1.6}, {1.8, 2.4}}, 1.0, 12))
}

func TestEffectiveCutsClampsToTheItem(t *testing.T) {
	assertCuts(t, []hls.Cut{{0, 1.9}, {10.1, 12}}, hls.EffectiveCuts([]hls.Cut{{-1, 2}, {10, 13}}, 1.0, 12))
}

func TestEffectiveCutsSnapsNearEdges(t *testing.T) {
	// An end within CutMargin of 0 or the duration is snapped, not shrunk,
	// so no sliver of media is left at the item's edge.
	assertCuts(t, []hls.Cut{{0, 1.9}}, hls.EffectiveCuts([]hls.Cut{{0.03, 2}}, 1.0, 12))
	assertCuts(t, []hls.Cut{{9.1, 12}}, hls.EffectiveCuts([]hls.Cut{{9, 11.95}}, 1.0, 12))
}

func TestEffectiveCutsUnsorted(t *testing.T) {
	assertCuts(t, []hls.Cut{{1.1, 2.9}, {5.1, 6.9}}, hls.EffectiveCuts([]hls.Cut{{5, 7}, {1, 3}}, 1.0, 12))
}

func TestEffectiveCutsDoesNotChangeItsInput(t *testing.T) {
	in := []hls.Cut{{5, 7}, {1, 3}}
	hls.EffectiveCuts(in, 1.0, 12)
	assert.Equal(t, []hls.Cut{{5, 7}, {1, 3}}, in)
}

func TestTimeMapping(t *testing.T) {
	cuts := []hls.Cut{{2, 4}, {6, 7}}
	assert.InDelta(t, 9.0, hls.PresentationDuration(cuts, 12), 1e-9)
	assert.InDelta(t, 1.0, hls.ItemTime(cuts, 1), 1e-9)
	assert.InDelta(t, 5.0, hls.ItemTime(cuts, 3), 1e-9)         // after the first cut
	assert.InDelta(t, 8.0, hls.ItemTime(cuts, 5), 1e-9)         // after both
	assert.InDelta(t, 2.0, hls.PresentationTime(cuts, 3), 1e-9) // inside a cut
	assert.InDelta(t, 5.0, hls.PresentationTime(cuts, 8), 1e-9)
	for _, p := range []float64{0, 1.5, 2, 3.25, 4, 8.9} {
		assert.InDelta(t, p, hls.PresentationTime(cuts, hls.ItemTime(cuts, p)), 1e-9, "round trip %v", p)
	}
}
