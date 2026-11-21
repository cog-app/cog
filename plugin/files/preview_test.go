package files_test

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// withOrientation returns a JPEG carrying an EXIF orientation tag.
func withOrientation(t *testing.T, img image.Image, orientation uint16) []byte {
	t.Helper()
	var enc bytes.Buffer
	require.NoError(t, jpeg.Encode(&enc, img, nil))

	var tiff bytes.Buffer
	tiff.WriteString("II*\x00")
	binary.Write(&tiff, binary.LittleEndian, uint32(8))
	binary.Write(&tiff, binary.LittleEndian, uint16(1))      // one entry
	binary.Write(&tiff, binary.LittleEndian, uint16(0x0112)) // Orientation
	binary.Write(&tiff, binary.LittleEndian, uint16(3))      // SHORT
	binary.Write(&tiff, binary.LittleEndian, uint32(1))
	binary.Write(&tiff, binary.LittleEndian, orientation)
	binary.Write(&tiff, binary.LittleEndian, uint16(0))
	binary.Write(&tiff, binary.LittleEndian, uint32(0)) // no next IFD
	app1 := append([]byte("Exif\x00\x00"), tiff.Bytes()...)

	out := append([]byte{}, enc.Bytes()[:2]...) // SOI
	out = append(out, 0xff, 0xe1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(app1)+2))
	out = append(out, app1...)
	return append(out, enc.Bytes()[2:]...)
}

// A phone stores a portrait photo as landscape pixels and a tag saying to
// turn it; the thumbnail must come out portrait, top side up.
func TestPreviewAppliesOrientation(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Stored 400×200: left half red, right half blue. Orientation 6 turns it
	// clockwise, so upright the red half is on top.
	img := image.NewRGBA(image.Rect(0, 0, 400, 200))
	for y := range 200 {
		for x := range 400 {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 200 {
				c = color.RGBA{0, 0, 255, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	env.PostText(t, "/api/files/upload?path=/portrait.jpg", string(withOrientation(t, img, 6)), alice.Auth).Status(201)

	body := env.Get(t, "/apps/files/portrait.jpg?preview=1&size=128", alice.Auth).Status(200).ReadBody()
	thumb, err := jpeg.Decode(strings.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 64, 128), thumb.Bounds())
	r, _, b, _ := thumb.At(32, 10).RGBA()
	assert.True(t, r > b, "top of the upright thumbnail should be red")
	r, _, b, _ = thumb.At(32, 118).RGBA()
	assert.True(t, b > r, "bottom of the upright thumbnail should be blue")
}
