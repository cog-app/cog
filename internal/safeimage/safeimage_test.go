package safeimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestDecodeSmall(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 3, 2))))
	img, format, err := Decode(&buf)
	require.NoError(t, err)
	require.Equal(t, "png", format)
	require.Equal(t, 3, img.Bounds().Dx())
}

// A PNG header may declare any size; the pixels need not follow. Decode
// must refuse it before allocating for them.
func TestDecodeRefusesHuge(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1, 1))))
	b := buf.Bytes()
	// IHDR width and height are big-endian at offsets 16 and 20.
	for _, off := range []int{16, 20} {
		b[off], b[off+1], b[off+2], b[off+3] = 0, 0, 0xea, 0x60 // 60000
	}
	// The chunk's CRC covers its type and data (bytes 12..29).
	binary.BigEndian.PutUint32(b[29:], crc32.ChecksumIEEE(b[12:29]))
	_, _, err := Decode(bytes.NewReader(b))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

// A phone's panorama is past 50 megapixels but, as a JPEG, well inside the
// budget; the same pixels as a 16-bit PNG are not.
func TestCheckCountsBytes(t *testing.T) {
	require.NoError(t, Check(image.Config{ColorModel: color.YCbCrModel, Width: 19696, Height: 3744}))
	err := Check(image.Config{ColorModel: color.RGBA64Model, Width: 19696, Height: 3744})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}
