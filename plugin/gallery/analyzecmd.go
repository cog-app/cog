package gallery

import (
	"context"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// analyzeFile runs one image through the scan's per-photo stages with the
// server's settings and prints what each found and how long it took, for
// tuning thresholds and finding out why a photo came out as it did. The
// working copy, with the faces found drawn on it, is written to a temporary
// file.
func (p *Plugin) analyzeFile(ctx context.Context, w io.Writer, path string) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return err
	}
	step := func(name string, fn func() error) error {
		start := time.Now()
		err := fn()
		fmt.Fprintf(w, "%-12s %8s", name, time.Since(start).Round(100*time.Microsecond))
		if err != nil {
			fmt.Fprintf(w, "  error: %v", err)
		}
		fmt.Fprintln(w)
		return err
	}

	var photo *decodedPhoto
	if err := step("decode", func() (err error) { photo, err = decodePhoto(path); return }); err != nil {
		return err
	}
	b := photo.decoded.Bounds()
	fmt.Fprintf(w, "  stored %d×%d, orientation %d, working copy %d×%d\n",
		b.Dx(), b.Dy(), photo.orient, photo.work.Rect.Dx(), photo.work.Rect.Dy())

	var quality float64
	step("quality", func() error { quality = laplacianVariance(photo.work); return nil })
	fmt.Fprintf(w, "  sharpness %.6f\n", quality)

	algorithm := SettingDuplicateAlgorithm.MustGet(sysDB)
	if hasher, err := getHasher(algorithm); err != nil {
		fmt.Fprintf(w, "fingerprint  error: %v\n", err)
	} else {
		var fp string
		step("fingerprint", func() error { fp = hex.EncodeToString([]byte(hasher.Calculate(photo.work))); return nil })
		fmt.Fprintf(w, "  %s %s\n", algorithm, fp)
	}

	var tags []RecognizedTag
	var faces []DetectedFace
	rec, release, err := p.recognizer(sysDB)
	defer release()
	switch {
	case err != nil:
		fmt.Fprintf(w, "recognition  error: %v\n", err)
	case rec == nil:
		fmt.Fprintln(w, "recognition  off")
	default:
		// The first run includes loading the models; the second is what a
		// scan pays per photo.
		step("recognition", func() (err error) { tags, faces, err = rec.run(ctx, photo, path); return })
		step("  again", func() (err error) { tags, faces, err = rec.run(ctx, photo, path); return })
		for _, t := range tags {
			mark := ""
			if t.Confidence < rec.tagMinConf {
				mark = "  (below tag_min_confidence, not stored)"
			}
			fmt.Fprintf(w, "  tag %-30s %.3f%s\n", t.Name, t.Confidence, mark)
		}
		fb := photo.Full().Bounds()
		for i, f := range faces {
			var notes []string
			if f.Confidence < rec.faceMinConf {
				notes = append(notes, "below face_min_confidence, not stored")
			}
			if f.Embedding == nil {
				notes = append(notes, "no embedding")
			}
			if f.Quality > 0 && f.Quality < seedFaceQuality {
				notes = append(notes, "too poor to found a person")
			}
			fmt.Fprintf(w, "  face %d at %d,%d %d×%d px  confidence %.3f  quality %.2f  age %.0f  %s\n",
				i+1, int(f.X*float64(fb.Dx())), int(f.Y*float64(fb.Dy())),
				int(f.W*float64(fb.Dx())), int(f.H*float64(fb.Dy())),
				f.Confidence, f.Quality, f.EstimatedAge, strings.Join(notes, "; "))
		}
	}

	m := &Media{MimeType: "image/" + strings.TrimPrefix(filepath.Ext(path), ".")}
	if likelyText(m, tags) {
		fmt.Fprintln(w, "ocr          would run (no camera EXIF is read here, so it always would)")
	} else {
		fmt.Fprintln(w, "ocr          skipped: tags do not suggest text")
	}
	if TesseractAvailable() {
		var text string
		if step("  tesseract", func() (err error) { text, err = tesseractOCR(path); return }) == nil {
			fmt.Fprintf(w, "  %q\n", text)
		}
	}

	out, err := os.CreateTemp("", "cog-analyze-*.jpg")
	if err != nil {
		return err
	}
	overlay := drawFaces(photo.work, faces)
	if err := jpeg.Encode(out, overlay, &jpeg.Options{Quality: 85}); err != nil {
		out.Close()
		return fmt.Errorf("write overlay: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("write overlay: %w", err)
	}
	fmt.Fprintf(w, "overlay      %s\n", out.Name())
	return nil
}

// drawFaces returns a copy of img with a box around each face: green for a
// face good enough to found a person, amber for one that is not.
func drawFaces(img *image.RGBA, faces []DetectedFace) *image.RGBA {
	out := image.NewRGBA(img.Rect)
	copy(out.Pix, img.Pix)
	w, h := float64(img.Rect.Dx()), float64(img.Rect.Dy())
	for _, f := range faces {
		c := color.RGBA{0, 220, 0, 255}
		if f.Quality < seedFaceQuality {
			c = color.RGBA{255, 170, 0, 255}
		}
		x0, y0 := int(f.X*w), int(f.Y*h)
		x1, y1 := int((f.X+f.W)*w), int((f.Y+f.H)*h)
		for t := range 2 {
			for x := x0; x <= x1; x++ {
				out.SetRGBA(x, y0+t, c)
				out.SetRGBA(x, y1-t, c)
			}
			for y := y0; y <= y1; y++ {
				out.SetRGBA(x0+t, y, c)
				out.SetRGBA(x1-t, y, c)
			}
		}
	}
	return out
}
