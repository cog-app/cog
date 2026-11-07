// Package safeimage decodes images that users uploaded. A PNG of a few
// hundred bytes can declare 60,000×60,000 pixels, and image.Decode would
// allocate the 14 GB that takes before finding out there is nothing in it.
// Decode reads the header first and refuses an image whose pixels would
// not fit in MaxPixels.
package safeimage

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"io"
)

// MaxPixels is the largest image Decode will decode: 50 megapixels, which
// is 200 MB as RGBA and more than any camera makes.
const MaxPixels = 50_000_000

// ErrTooLarge is an image whose dimensions exceed MaxPixels.
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

// Check refuses a configuration whose image would exceed MaxPixels.
func Check(cfg image.Config) error {
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return fmt.Errorf("%w: %d×%d pixels", ErrTooLarge, cfg.Width, cfg.Height)
	}
	return nil
}
