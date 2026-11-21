package gallery

import (
	"image"
	"image/color"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestLikelyText(t *testing.T) {
	camera := &Media{MimeType: "image/jpeg", CameraModel: "Pixel 7"}
	require.False(t, likelyText(camera, nil))
	require.False(t, likelyText(camera, []RecognizedTag{{Name: "dog"}}))
	require.True(t, likelyText(camera, []RecognizedTag{{Name: "street sign"}}))
	require.True(t, likelyText(&Media{MimeType: "image/jpeg"}, nil), "no camera: a screenshot or a scan")
	require.True(t, likelyText(&Media{MimeType: "image/png", CameraModel: "x"}, nil))
}

func TestLaplacianVarianceRanksSharpness(t *testing.T) {
	sharp := image.NewRGBA(image.Rect(0, 0, 64, 64))
	flat := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			v := uint8(0)
			if (x/2+y/2)%2 == 0 {
				v = 255
			}
			sharp.SetRGBA(x, y, color.RGBA{v, v, v, 255})
			flat.SetRGBA(x, y, color.RGBA{128, 128, 128, 255})
		}
	}
	require.True(t, laplacianVariance(sharp) > laplacianVariance(flat))
	require.Equal(t, 0.0, laplacianVariance(flat))
}

// The working copy is upright and no larger than workSize.
func TestDecodedPhotoWorkCopy(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 4000, 3000))
	d := newDecodedPhoto("", img, 6)
	require.Equal(t, image.Rect(0, 0, 768, 1024), d.work.Rect)
	require.Equal(t, image.Rect(0, 0, 3000, 4000), d.Full().Bounds())
}

func TestScanRoots(t *testing.T) {
	require.Equal(t, []string{"/InstantUpload", "/Photos"}, scanRoots([]string{"Photos/", "InstantUpload/", "Photos/2024/"}))
	require.Equal(t, []string{"/Photos"}, scanRoots([]string{"Photos/*.jpg"}))
	require.Equal(t, []string{"/"}, scanRoots([]string{"*.jpg", "Photos/"}))
	require.Equal(t, []string{"/Photo", "/Photos"}, scanRoots([]string{"Photos/", "Photo/"}))
}
