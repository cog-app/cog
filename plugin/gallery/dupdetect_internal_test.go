package gallery

import (
	"image"
	"image/color"
	"math/rand/v2"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func entry(id int64, path string, fp uint64, w, h int) dupEntry {
	b := make([]byte, 8)
	for i := range b {
		b[i] = byte(fp >> (8 * i))
	}
	return dupEntry{ID: id, Path: path, Fingerprint: "x", Width: w, Height: h, words: packWords(b)}
}

func groupsByKind(groups []duplicateGroup) map[DuplicateKind][][]int64 {
	out := map[DuplicateKind][][]int64{}
	for _, g := range groups {
		var ids []int64
		for _, m := range g.members {
			ids = append(ids, m.mediaID)
		}
		out[g.kind] = append(out[g.kind], ids)
	}
	return out
}

func noScore(a, b *dupEntry) (float64, bool) { return 0, false }

// A photo joins a group only if it is close to every member: A is like B
// and B like C, but A and C differ, so the three are not one group.
func TestPlanDuplicatesDoesNotChain(t *testing.T) {
	entries := []dupEntry{
		entry(1, "/a.jpg", 0b0000, 10, 10),
		entry(2, "/b.jpg", 0b0011, 10, 10),
		entry(3, "/c.jpg", 0b1111, 10, 10),
	}
	groups := planDuplicateGroups(entries, 2, nil, nil, noScore)
	require.Equal(t, 1, len(groups))
	require.Equal(t, 2, len(groups[0].members))
}

// Identical files are grouped first, keeping the name that does not look
// copied; the kept file then stands for them, so a scaled-down copy of it
// is grouped with it as smaller, and a burst shot as similar to that.
func TestPlanDuplicatesSortsByKind(t *testing.T) {
	entries := []dupEntry{
		entry(1, "/Photos/IMG_2024_1.jpg", 0, 400, 300),
		entry(2, "/Photos/IMG_2024.jpg", 0, 400, 300),
		entry(3, "/Photos/small.jpg", 0b1, 200, 150),
		entry(4, "/Photos/PXL_burst_shot.jpg", 0b11, 400, 300),
		entry(5, "/Photos/other.jpg", ^uint64(0), 400, 300),
	}
	entries[0].Hash, entries[1].Hash = "same", "same"
	score := func(a, b *dupEntry) (float64, bool) {
		if (a.ID == 3 || b.ID == 3) && (a.ID <= 2 || b.ID <= 2) {
			return 45, true
		}
		return 20, true
	}
	groups := planDuplicateGroups(entries, 3, nil, nil, score)
	byKind := groupsByKind(groups)
	assert.Equal(t, [][]int64{{1, 2}}, byKind[DuplicateIdentical])
	assert.Equal(t, [][]int64{{2, 3}}, byKind[DuplicateSmaller])
	assert.Equal(t, [][]int64{{2, 4}}, byKind[DuplicateSimilar])
	for _, g := range groups {
		assert.Equal(t, int64(2), g.keep, "group %v keeps %d", g.members, g.keep)
	}
}

// Someone's choice of what to keep, and their word that two photos are not
// duplicates, outrank the scores.
func TestPlanDuplicatesHonoursDecisions(t *testing.T) {
	entries := []dupEntry{
		entry(1, "/a.jpg", 0, 400, 300),
		entry(2, "/a_1.jpg", 0, 400, 300),
		entry(3, "/b.jpg", 0, 400, 300),
	}
	for i := range entries {
		entries[i].Hash = "same"
	}
	groups := planDuplicateGroups(entries, 0, map[[2]int64]bool{{1, 3}: true}, map[int64]bool{2: true}, noScore)
	byKind := groupsByKind(groups)
	require.Equal(t, 1, len(byKind[DuplicateIdentical]))
	require.Equal(t, int64(2), groups[0].keep)
	assert.False(t, len(groups[0].members) == 3, "an excluded pair shares a group")
}

func TestIsCopyName(t *testing.T) {
	for name, want := range map[string]bool{
		"/P/IMG_1234.jpg":                 false,
		"/P/PXL_20240723_231835756.jpg":   false,
		"/P/PXL_20240723_231835756_1.jpg": true,
		"/P/DSCN0047(1).JPG":              true,
		"/P/DSCN0490-001.JPG":             true,
		"/P/photo - Copy.jpg":             true,
	} {
		assert.Equal(t, want, isCopyName(name), name)
	}
}

func noisy(seed uint64, w, h int) image.Image {
	r := rand.New(rand.NewPCG(seed, seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := uint8(x*255/w) ^ uint8(r.IntN(64))
			img.SetRGBA(x, y, color.RGBA{v, uint8(y * 255 / h), v / 2, 255})
		}
	}
	return img
}

// A scaled copy scores as the same picture, a different photo does not,
// and a blank one cannot be told apart from anything, so is not scored.
func TestThumbScore(t *testing.T) {
	big := noisy(1, 256, 192)
	assert.Equal(t, float64(identicalScore), thumbScore(big, big))
	assert.True(t, thumbScore(big, imaging.Resize(big, 128, 96)) >= copyScore, "scaled copy")
	assert.True(t, thumbScore(big, noisy(2, 256, 192)) < copyScore, "different photo")
	blank := image.NewRGBA(image.Rect(0, 0, 256, 192))
	assert.Equal(t, -1.0, thumbScore(blank, blank))
}
