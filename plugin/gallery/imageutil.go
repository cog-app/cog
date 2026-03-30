package gallery

import (
	"image"
	"image/color"
	"math"
)

// cropImage extracts a sub-region from an image.
func cropImage(img image.Image, rect image.Rectangle) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			var c color.Color
			if x >= img.Bounds().Min.X && x < img.Bounds().Max.X &&
				y >= img.Bounds().Min.Y && y < img.Bounds().Max.Y {
				c = img.At(x, y)
			} else {
				c = color.Black
			}
			dst.Set(x-rect.Min.X, y-rect.Min.Y, c)
		}
	}
	return dst
}

// l2normalize normalizes a vector to unit length.
func l2normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	norm := float32(math.Sqrt(sum))
	if norm == 0 {
		return
	}
	for i := range v {
		v[i] /= norm
	}
}
