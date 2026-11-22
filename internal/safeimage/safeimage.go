// Package safeimage decodes images that users uploaded. A PNG of a few
// hundred bytes can declare 60,000×60,000 pixels, and image.Decode would
// allocate the 14 GB that takes before finding out there is nothing in it.
// Decode reads the header first and refuses an image whose pixels would
// not fit in MaxBytes.
package safeimage

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
)

// MaxBytes is the most memory Decode lets an image's pixels take: 50
// megapixels of 16-bit RGBA, or a 130-megapixel JPEG. A phone's panorama
// runs to 80 megapixels, which a pixel count low enough to stop a 16-bit
// PNG would refuse.
const MaxBytes = 400_000_000

// ErrTooLarge is an image whose pixels would exceed MaxBytes.
var ErrTooLarge = errors.New("image is too large to decode")

// Decode is image.Decode for an image that is not trusted. It works on any
// reader: the bytes the header took are kept and read again before the
// rest.
func Decode(r io.Reader) (image.Image, string, error) {
	var hdr bytes.Buffer
	cfg, format, err := image.DecodeConfig(io.TeeReader(r, &hdr))
	if err != nil {
		return nil, "", err
	}
	if err := Check(cfg); err != nil {
		return nil, format, err
	}
	return image.Decode(io.MultiReader(&hdr, r))
}

// Check refuses a configuration whose pixels would exceed MaxBytes.
func Check(cfg image.Config) error {
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height)*bytesPerPixel(cfg.ColorModel) > MaxBytes {
		return fmt.Errorf("%w: %d×%d pixels", ErrTooLarge, cfg.Width, cfg.Height)
	}
	return nil
}

// bytesPerPixel is what a decoder allocates per pixel for a colour model,
// erring high: a JPEG's chroma may be subsampled, but is counted whole.
func bytesPerPixel(m color.Model) int64 {
	switch m {
	case color.GrayModel, color.AlphaModel:
		return 1
	case color.Gray16Model, color.Alpha16Model:
		return 2
	case color.YCbCrModel:
		return 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel, color.NYCbCrAModel:
		return 4
	}
	if _, ok := m.(color.Palette); ok {
		return 1
	}
	return 8
}
