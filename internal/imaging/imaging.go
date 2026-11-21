// Package imaging decodes photos upright and scales them down quickly:
// Resize averages whole pixel blocks down to under twice the target before
// applying a high-quality kernel, whose cost grows with the scale factor.
package imaging

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"io"

	"github.com/rwcarlsen/goexif/exif"
	xdraw "golang.org/x/image/draw"

	"git.duckfam.us/jj-software/cog/internal/safeimage"
)

// Orientation returns the EXIF orientation of an image, 1 through 8, or 1
// when it has none.
func Orientation(r io.Reader) int {
	x, err := exif.Decode(r)
	if err != nil {
		return 1
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return 1
	}
	o, err := tag.Int(0)
	if err != nil || o < 1 || o > 8 {
		return 1
	}
	return o
}

// Decode decodes an untrusted image (see safeimage) and reports its EXIF
// orientation, which the caller applies with Orient once it has scaled the
// image down, when turning it is cheapest.
func Decode(data []byte) (img image.Image, format string, orientation int, err error) {
	img, format, err = safeimage.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, format, 1, err
	}
	orientation = 1
	if format == "jpeg" || format == "webp" {
		orientation = Orientation(bytes.NewReader(data))
	}
	return img, format, orientation, nil
}

// OrientedSize is the size of a w×h image once turned by orientation.
func OrientedSize(w, h, orientation int) (int, int) {
	if orientation >= 5 {
		return h, w
	}
	return w, h
}

// Orient turns an image upright according to its EXIF orientation.
// Orientation 1 returns img itself.
func Orient(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}
	src := ToRGBA(img)
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dw, dh := OrientedSize(sw, sh, orientation)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range sh {
		row := src.Pix[y*src.Stride : y*src.Stride+sw*4]
		for x := range sw {
			var dx, dy int
			switch orientation {
			case 2: // mirrored
				dx, dy = sw-1-x, y
			case 3: // rotated 180°
				dx, dy = sw-1-x, sh-1-y
			case 4: // mirrored vertically
				dx, dy = x, sh-1-y
			case 5: // transposed
				dx, dy = y, x
			case 6: // rotated 90° clockwise to view
				dx, dy = sh-1-y, x
			case 7: // transversed
				dx, dy = sh-1-y, sw-1-x
			case 8: // rotated 90° counter-clockwise to view
				dx, dy = y, sw-1-x
			}
			copy(dst.Pix[dy*dst.Stride+dx*4:dy*dst.Stride+dx*4+4], row[x*4:x*4+4])
		}
	}
	return dst
}

// Fit returns the largest size within size×size with the aspect ratio of
// a w×h image, never larger than the image itself.
func Fit(w, h, size int) (int, int) {
	if w <= size && h <= size {
		return w, h
	}
	if w >= h {
		return size, max(1, h*size/w)
	}
	return max(1, w*size/h), size
}

// Resize scales src to exactly w×h.
func Resize(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	if k := min(b.Dx()/w, b.Dy()/h); k >= 2 {
		src = BoxShrink(src, k)
		b = src.Bounds()
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if b.Dx() == w && b.Dy() == h {
		draw.Draw(dst, dst.Rect, src, b.Min, draw.Src)
		return dst
	}
	xdraw.CatmullRom.Scale(dst, dst.Rect, src, b, xdraw.Src, nil)
	return dst
}

// Shrink scales src to fit within size×size, keeping its aspect ratio. An
// image already that small is converted, not scaled.
func Shrink(src image.Image, size int) *image.RGBA {
	b := src.Bounds()
	w, h := Fit(b.Dx(), b.Dy(), size)
	if w == b.Dx() && h == b.Dy() {
		return ToRGBA(src)
	}
	return Resize(src, w, h)
}

// ToRGBA returns img as an *image.RGBA with its origin at 0,0, converting
// only when it is not one already.
func ToRGBA(img image.Image) *image.RGBA {
	if r, ok := img.(*image.RGBA); ok && r.Rect.Min == (image.Point{}) {
		return r
	}
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Rect, img, b.Min, draw.Src)
	return dst
}

// BoxShrink divides src's size by k, each output pixel the mean of the
// k×k block it covers. A block at the right or bottom edge covers what is
// left, so the whole image is kept.
func BoxShrink(src image.Image, k int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := (sw+k-1)/k, (sh+k-1)/k
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	acc := make([]uint32, dw*4)
	row := make([]uint8, sw*4)
	for oy := range dh {
		clear(acc)
		y0, y1 := oy*k, min(oy*k+k, sh)
		for y := y0; y < y1; y++ {
			readRow(src, b, y, row)
			for x := range sw {
				i := (x / k) * 4
				acc[i] += uint32(row[x*4])
				acc[i+1] += uint32(row[x*4+1])
				acc[i+2] += uint32(row[x*4+2])
				acc[i+3] += uint32(row[x*4+3])
			}
		}
		out := dst.Pix[oy*dst.Stride:]
		for ox := range dw {
			n := uint32((min(ox*k+k, sw) - ox*k) * (y1 - y0))
			i := ox * 4
			out[i] = uint8((acc[i] + n/2) / n)
			out[i+1] = uint8((acc[i+1] + n/2) / n)
			out[i+2] = uint8((acc[i+2] + n/2) / n)
			out[i+3] = uint8((acc[i+3] + n/2) / n)
		}
	}
	return dst
}

// readRow writes row y of src (relative to b) into row as premultiplied
// RGBA, with fast paths for what decoders return for photos.
func readRow(src image.Image, b image.Rectangle, y int, row []uint8) {
	sw := b.Dx()
	sy := b.Min.Y + y
	switch s := src.(type) {
	case *image.YCbCr:
		yi := s.YOffset(b.Min.X, sy)
		for x := range sw {
			ci := s.COffset(b.Min.X+x, sy)
			r, g, bl := color.YCbCrToRGB(s.Y[yi+x], s.Cb[ci], s.Cr[ci])
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = r, g, bl, 0xff
		}
	case *image.RGBA:
		i := s.PixOffset(b.Min.X, sy)
		copy(row, s.Pix[i:i+sw*4])
	case *image.NRGBA:
		i := s.PixOffset(b.Min.X, sy)
		for x := range sw {
			p := s.Pix[i+x*4 : i+x*4+4]
			a := uint32(p[3])
			row[x*4] = uint8(uint32(p[0]) * a / 0xff)
			row[x*4+1] = uint8(uint32(p[1]) * a / 0xff)
			row[x*4+2] = uint8(uint32(p[2]) * a / 0xff)
			row[x*4+3] = p[3]
		}
	case *image.Gray:
		i := s.PixOffset(b.Min.X, sy)
		for x := range sw {
			v := s.Pix[i+x]
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = v, v, v, 0xff
		}
	default:
		for x := range sw {
			r, g, bl, a := src.At(b.Min.X+x, sy).RGBA()
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8)
		}
	}
}
