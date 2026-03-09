package gallery

import (
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	_ "golang.org/x/image/webp"
)

// Model download URLs.
const (
	mobilenetURL = "https://github.com/onnx/models/raw/main/validated/vision/classification/mobilenet/model/mobilenetv2-12-int8.onnx"
	arcfaceURL   = "https://github.com/onnx/models/raw/main/validated/vision/body_analysis/arcface/model/arcfaceresnet100-11-int8.onnx"

	mobilenetFile = "mobilenetv2-12-int8.onnx"
	arcfaceFile   = "arcfaceresnet100-11-int8.onnx"
)

// OnnxBackend uses pigo for face detection, ArcFace ONNX for face embeddings,
// and MobileNetV2 ONNX for image classification tags.
type OnnxBackend struct {
	ModelsDir   string // directory to store/load ONNX model files
	LibraryPath string // path to libonnxruntime.so

	once       sync.Once
	initErr    error
	pigo       *PigoBackend
	hasArcface bool
}

func (b *OnnxBackend) Name() string { return "ONNX (local)" }

func (b *OnnxBackend) Available() bool {
	if b.LibraryPath == "" {
		for _, p := range []string{
			"/usr/lib/libonnxruntime.so",
			"/usr/lib64/libonnxruntime.so",
			"/usr/local/lib/libonnxruntime.so",
		} {
			if _, err := os.Stat(p); err == nil {
				b.LibraryPath = p
				break
			}
		}
	}
	if b.LibraryPath == "" {
		return false
	}
	_, err := os.Stat(b.LibraryPath)
	return err == nil
}

func (b *OnnxBackend) init() {
	b.once.Do(func() {
		b.pigo = &PigoBackend{}

		ort.SetSharedLibraryPath(b.LibraryPath)
		if err := ort.InitializeEnvironment(); err != nil {
			if err.Error() != "The ONNX runtime environment has already been initialized" {
				b.initErr = fmt.Errorf("init onnxruntime: %w", err)
				return
			}
		}

		if err := os.MkdirAll(b.ModelsDir, 0o755); err != nil {
			b.initErr = fmt.Errorf("create models dir: %w", err)
			return
		}

		// Ensure MobileNet is downloaded.
		mobilenetPath := filepath.Join(b.ModelsDir, mobilenetFile)
		if err := ensureModel(mobilenetPath, mobilenetURL); err != nil {
			b.initErr = fmt.Errorf("mobilenet model: %w", err)
			return
		}

		// ArcFace is optional.
		arcfacePath := filepath.Join(b.ModelsDir, arcfaceFile)
		if err := ensureModel(arcfacePath, arcfaceURL); err != nil {
			slog.Warn("arcface model unavailable", "err", err)
		} else {
			b.hasArcface = true
		}
	})
}

func ensureModel(path, url string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	slog.Info("downloading model", "model", filepath.Base(path))
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", url, resp.StatusCode)
	}

	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

func (b *OnnxBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	b.init()
	if b.initErr != nil {
		return nil, b.initErr
	}

	f, err := os.Open(imagePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}

	result := &RecognitionResult{}

	// MobileNet classification on full image.
	tags, err := b.classifyImage(img)
	if err != nil {
		slog.Warn("onnx classify", "path", imagePath, "err", err)
	} else {
		result.Tags = tags
	}

	// Face detection via pigo, then ArcFace embeddings.
	pigoResult, err := b.pigo.Analyze(ctx, imagePath)
	if err != nil {
		slog.Warn("onnx face detect", "path", imagePath, "err", err)
	} else if b.hasArcface {
		for _, face := range pigoResult.Faces {
			embedding, err := b.computeFaceEmbedding(img, &face)
			if err != nil {
				slog.Warn("onnx arcface", "err", err)
				result.Faces = append(result.Faces, face)
				continue
			}
			face.Embedding = embedding
			result.Faces = append(result.Faces, face)
		}
	} else {
		result.Faces = pigoResult.Faces
	}

	return result, nil
}

// classifyImage runs MobileNetV2 on the image and returns top-5 tags.
func (b *OnnxBackend) classifyImage(img image.Image) ([]RecognizedTag, error) {
	data := preprocessImage(img, 224, 224)

	inputTensor, err := ort.NewTensor(ort.NewShape(1, 3, 224, 224), data)
	if err != nil {
		return nil, fmt.Errorf("create input tensor: %w", err)
	}
	defer inputTensor.Destroy()

	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 1000))
	if err != nil {
		return nil, fmt.Errorf("create output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	session, err := ort.NewAdvancedSession(
		filepath.Join(b.ModelsDir, mobilenetFile),
		[]string{"input"}, []string{"output"},
		[]ort.Value{inputTensor}, []ort.Value{outputTensor}, nil)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	defer session.Destroy()

	if err := session.Run(); err != nil {
		return nil, fmt.Errorf("run mobilenet: %w", err)
	}

	output := outputTensor.GetData()
	probs := softmax(output)

	type indexedProb struct {
		index int
		prob  float32
	}
	indexed := make([]indexedProb, len(probs))
	for i, p := range probs {
		indexed[i] = indexedProb{i, p}
	}
	sort.Slice(indexed, func(i, j int) bool {
		return indexed[i].prob > indexed[j].prob
	})

	var tags []RecognizedTag
	for i := 0; i < 5 && i < len(indexed); i++ {
		if indexed[i].prob < 0.05 {
			break
		}
		label := "unknown"
		if indexed[i].index < len(imagenetLabels) {
			label = imagenetLabels[indexed[i].index]
		}
		tags = append(tags, RecognizedTag{
			Name:       label,
			Confidence: float64(indexed[i].prob),
			Type:       1,
		})
	}
	return tags, nil
}

// computeFaceEmbedding crops a face from the image and runs ArcFace to get an embedding.
func (b *OnnxBackend) computeFaceEmbedding(img image.Image, face *DetectedFace) ([]float32, error) {
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	x := int(face.X * imgW)
	y := int(face.Y * imgH)
	w := int(face.W * imgW)
	h := int(face.H * imgH)

	// Add 20% padding for better face crop.
	padW := w / 5
	padH := h / 5
	x = max(0, x-padW)
	y = max(0, y-padH)
	w = min(bounds.Dx()-x, w+2*padW)
	h = min(bounds.Dy()-y, h+2*padH)

	if w < 10 || h < 10 {
		return nil, fmt.Errorf("face crop too small: %dx%d", w, h)
	}

	faceCrop := cropImage(img, image.Rect(x, y, x+w, y+h))
	data := preprocessImage(faceCrop, 112, 112)

	inputTensor, err := ort.NewTensor(ort.NewShape(1, 3, 112, 112), data)
	if err != nil {
		return nil, fmt.Errorf("create face tensor: %w", err)
	}
	defer inputTensor.Destroy()

	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 512))
	if err != nil {
		return nil, fmt.Errorf("create face output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	session, err := ort.NewAdvancedSession(
		filepath.Join(b.ModelsDir, arcfaceFile),
		[]string{"data"}, []string{"fc1"},
		[]ort.Value{inputTensor}, []ort.Value{outputTensor}, nil)
	if err != nil {
		return nil, fmt.Errorf("create arcface session: %w", err)
	}
	defer session.Destroy()

	if err := session.Run(); err != nil {
		return nil, fmt.Errorf("run arcface: %w", err)
	}

	output := outputTensor.GetData()
	embedding := make([]float32, len(output))
	copy(embedding, output)
	l2normalize(embedding)

	return embedding, nil
}

// preprocessImage resizes an image to targetW x targetH and converts to CHW float32 tensor
// with ImageNet normalization (mean=[0.485,0.456,0.406], std=[0.229,0.224,0.225]).
func preprocessImage(img image.Image, targetW, targetH int) []float32 {
	resized := resizeImage(img, targetW, targetH)

	tensor := make([]float32, 3*targetH*targetW)
	mean := [3]float32{0.485, 0.456, 0.406}
	std := [3]float32{0.229, 0.224, 0.225}

	for y := range targetH {
		for x := range targetW {
			r, g, b, _ := resized.At(x, y).RGBA()
			rf := float32(r) / 65535.0
			gf := float32(g) / 65535.0
			bf := float32(b) / 65535.0

			tensor[0*targetH*targetW+y*targetW+x] = (rf - mean[0]) / std[0]
			tensor[1*targetH*targetW+y*targetW+x] = (gf - mean[1]) / std[1]
			tensor[2*targetH*targetW+y*targetW+x] = (bf - mean[2]) / std[2]
		}
	}
	return tensor
}

// resizeImage performs nearest-neighbor resize.
func resizeImage(img image.Image, w, h int) image.Image {
	bounds := img.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			sx := int(float64(x) * float64(srcW) / float64(w))
			sy := int(float64(y) * float64(srcH) / float64(h))
			if sx >= srcW {
				sx = srcW - 1
			}
			if sy >= srcH {
				sy = srcH - 1
			}
			dst.Set(x, y, img.At(bounds.Min.X+sx, bounds.Min.Y+sy))
		}
	}
	return dst
}

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

// softmax applies the softmax function to logits.
func softmax(logits []float32) []float32 {
	maxVal := logits[0]
	for _, v := range logits[1:] {
		if v > maxVal {
			maxVal = v
		}
	}
	probs := make([]float32, len(logits))
	var sum float32
	for i, v := range logits {
		probs[i] = float32(math.Exp(float64(v - maxVal)))
		sum += probs[i]
	}
	for i := range probs {
		probs[i] /= sum
	}
	return probs
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
