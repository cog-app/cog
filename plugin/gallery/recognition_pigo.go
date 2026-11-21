package gallery

import (
	"context"
	_ "embed"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"sync"

	pigo "github.com/esimov/pigo/core"
	_ "golang.org/x/image/webp"
)

//go:embed cascade/facefinder
var facefinderCascade []byte

// PigoBackend uses the pigo library for pure-Go face detection.
// It detects face bounding boxes but does not produce embeddings or tags.
type PigoBackend struct {
	once     sync.Once
	cascade  *pigo.Pigo
	initErr  error
	minSize  int
	maxSize  int
	minScore float32
}

func (b *PigoBackend) Name() string    { return "Pigo (local face detection)" }
func (b *PigoBackend) Available() bool { return true }

func (b *PigoBackend) init() {
	b.once.Do(func() {
		p := pigo.NewPigo()
		b.cascade, b.initErr = p.Unpack(facefinderCascade)
	})
}

func (b *PigoBackend) Analyze(_ context.Context, imagePath string) (*RecognitionResult, error) {
	b.init()
	if b.initErr != nil {
		return nil, b.initErr
	}

	photo, err := decodePhoto(imagePath)
	if err != nil {
		return nil, err
	}
	faces, err := b.detect(photo.work)
	if err != nil {
		return nil, err
	}
	return &RecognitionResult{Faces: faces}, nil
}

// AnalyzeImage finds faces in the photo's working copy.
func (b *PigoBackend) AnalyzeImage(_ context.Context, photo *decodedPhoto) (*RecognitionResult, error) {
	faces, err := b.detect(photo.work)
	if err != nil {
		return nil, err
	}
	return &RecognitionResult{Faces: faces}, nil
}

// detect finds faces in img, in coordinates normalised to its size.
func (b *PigoBackend) detect(img image.Image) ([]DetectedFace, error) {
	b.init()
	if b.initErr != nil {
		return nil, b.initErr
	}
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()
	pixels := pigo.RgbToGrayscale(img)

	minSize := b.minSize
	if minSize == 0 {
		minSize = 20
	}
	maxSize := b.maxSize
	if maxSize == 0 {
		maxSize = min(width, height)
	}

	params := pigo.CascadeParams{
		MinSize:     minSize,
		MaxSize:     maxSize,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{
			Pixels: pixels,
			Rows:   height,
			Cols:   width,
			Dim:    width,
		},
	}

	dets := b.cascade.RunCascade(params, 0.0)
	dets = b.cascade.ClusterDetections(dets, 0.2)

	minScore := b.minScore
	if minScore == 0 {
		minScore = 5.0
	}

	var faces []DetectedFace
	for _, d := range dets {
		if d.Q < minScore {
			continue
		}
		// Convert pigo detection (row, col, scale = center + diameter) to normalized bounding box.
		cx := float64(d.Col) / float64(width)
		cy := float64(d.Row) / float64(height)
		s := float64(d.Scale)
		w := s / float64(width)
		h := s / float64(height)

		faces = append(faces, DetectedFace{
			X:          cx - w/2,
			Y:          cy - h/2,
			W:          w,
			H:          h,
			Confidence: float64(d.Q) / 20.0, // normalize score roughly to 0-1
		})
	}
	return faces, nil
}
