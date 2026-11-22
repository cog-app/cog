package imgconv

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/imaging"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8(x ^ y), 255})
		}
	}
	return img
}

// Go's decoder refuses a JPEG cut short; ffmpeg shows what is there.
func TestDecodeTruncatedJPEG(t *testing.T) {
	if !Handles("image/jpeg") {
		t.Skip("ffmpeg not installed")
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, gradient(320, 240), nil))
	path := filepath.Join(t.TempDir(), "cut.jpg")
	require.NoError(t, os.WriteFile(path, buf.Bytes()[:buf.Len()*2/3], 0o644))
	_, _, _, err := imaging.Decode(buf.Bytes()[:buf.Len()*2/3])
	require.Error(t, err)

	img, orientation, err := Decode(context.Background(), path)
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 320, 240), img.Bounds())
	require.Equal(t, 1, orientation)
}

func TestDecodeHEIF(t *testing.T) {
	enc, err := exec.LookPath("heif-enc")
	if err != nil || !HandlesHEIF() {
		t.Skip("libheif tools not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.png")
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, gradient(64, 48)))
	require.NoError(t, os.WriteFile(src, buf.Bytes(), 0o644))
	heic := filepath.Join(dir, "photo.heic")
	out, err := exec.Command(enc, "-o", heic, src).CombinedOutput()
	require.NoError(t, err, string(out))

	img, orientation, err := Decode(context.Background(), heic)
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 64, 48), img.Bounds())
	require.Equal(t, 1, orientation)
}

func TestDecodeUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.txt")
	require.NoError(t, os.WriteFile(path, []byte("not an image at all"), 0o644))
	_, _, err := Decode(context.Background(), path)
	require.True(t, errors.Is(err, ErrUnsupported))
}
