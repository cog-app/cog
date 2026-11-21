package imaging

import (
	"image"
	"image/color"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
)

// A 3×2 image whose pixels are numbered, so a turned image says which
// source pixel landed where.
func numbered() *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 3, 2))
	for y := range 2 {
		for x := range 3 {
			img.SetRGBA(x, y, color.RGBA{uint8(y*3 + x), 0, 0, 255})
		}
	}
	return img
}

func rows(img image.Image) [][]uint8 {
	b := img.Bounds()
	out := make([][]uint8, b.Dy())
	for y := range b.Dy() {
		for x := range b.Dx() {
			r, _, _, _ := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			out[y] = append(out[y], uint8(r>>8))
		}
	}
	return out
}

func TestOrient(t *testing.T) {
	// 0 1 2
	// 3 4 5
	for _, tc := range []struct {
		orientation int
		want        [][]uint8
	}{
		{1, [][]uint8{{0, 1, 2}, {3, 4, 5}}},
		{2, [][]uint8{{2, 1, 0}, {5, 4, 3}}},
		{3, [][]uint8{{5, 4, 3}, {2, 1, 0}}},
		{4, [][]uint8{{3, 4, 5}, {0, 1, 2}}},
		{5, [][]uint8{{0, 3}, {1, 4}, {2, 5}}},
		{6, [][]uint8{{3, 0}, {4, 1}, {5, 2}}},
		{7, [][]uint8{{5, 2}, {4, 1}, {3, 0}}},
		{8, [][]uint8{{2, 5}, {1, 4}, {0, 3}}},
	} {
		assert.Equal(t, tc.want, rows(Orient(numbered(), tc.orientation)), "orientation %d", tc.orientation)
	}
}

func TestBoxShrinkKeepsEdges(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 5, 3))
	for i := range img.Pix {
		img.Pix[i] = 100
	}
	img.SetGray(4, 2, color.Gray{200})
	got := BoxShrink(img, 2)
	assert.Equal(t, image.Rect(0, 0, 3, 2), got.Rect)
	// The bottom-right block covers one pixel only.
	assert.Equal(t, [][]uint8{{100, 100, 100}, {100, 100, 200}}, rows(got))
}

func TestBoxShrinkYCbCrMatchesConversion(t *testing.T) {
	src := image.NewYCbCr(image.Rect(0, 0, 4, 4), image.YCbCrSubsampleRatio420)
	for i := range src.Y {
		src.Y[i] = 120
	}
	for i := range src.Cb {
		src.Cb[i], src.Cr[i] = 90, 170
	}
	r, g, b := color.YCbCrToRGB(120, 90, 170)
	got := BoxShrink(src, 4).RGBAAt(0, 0)
	assert.Equal(t, color.RGBA{r, g, b, 255}, got)
}

func TestShrink(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	assert.Equal(t, image.Rect(0, 0, 256, 192), Shrink(src, 256).Rect)
	assert.Equal(t, image.Rect(0, 0, 96, 128), Shrink(image.NewRGBA(image.Rect(0, 0, 300, 400)), 128).Rect)
	small := image.NewRGBA(image.Rect(0, 0, 20, 10))
	assert.Equal(t, small, Shrink(small, 256))
}
