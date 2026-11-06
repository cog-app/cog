//go:build linux

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
	"strings"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
	_ "golang.org/x/image/webp"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// modelHTTP downloads models. Their URLs are constants, but the models are
// large, so the client has no overall timeout; safehttp's idle timeout still
// gives up on a server that stops sending.
var modelHTTP = safehttp.Client(0)

// Model download URLs.
const (
	mobilenetURL  = "https://github.com/onnx/models/raw/main/validated/vision/classification/mobilenet/model/mobilenetv2-12-int8.onnx"
	joytagURL     = "https://huggingface.co/fancyfeast/joytag/resolve/main/model.onnx"
	joytagTagsURL = "https://huggingface.co/fancyfeast/joytag/resolve/main/top_tags.txt"
	wd14URL       = "https://huggingface.co/SmilingWolf/wd-convnext-tagger-v3/resolve/main/model.onnx"
	wd14TagsURL   = "https://huggingface.co/SmilingWolf/wd-convnext-tagger-v3/resolve/main/selected_tags.csv"
	arcfaceURL    = "https://huggingface.co/public-data/insightface/resolve/main/models/buffalo_l/w600k_r50.onnx"
	scrfdURL      = "https://huggingface.co/public-data/insightface/resolve/main/models/buffalo_l/det_10g.onnx"
	genderageURL  = "https://huggingface.co/public-data/insightface/resolve/main/models/buffalo_l/genderage.onnx"

	mobilenetFile  = "mobilenetv2-12-int8.onnx"
	joytagFile     = "joytag.onnx"
	joytagTagsFile = "joytag_tags.txt"
	wd14File       = "wd14.onnx"
	wd14TagsFile   = "wd14_tags.csv"
	arcfaceFile    = "w600k_r50.onnx"
	scrfdFile      = "det_10g.onnx"
	genderageFile  = "genderage.onnx"

	scrfdInputSize      = 640
	scrfdScoreThreshold = 0.5
	scrfdNMSThreshold   = 0.4
	scrfdNumAnchors     = 2
)

// SCRFD output tensor names (from model inspection, in graph order).
// 3 score tensors, 3 box tensors, 3 keypoint tensors — one per stride (8, 16, 32).
var (
	scrfdOutputNames = []string{"448", "471", "494", "451", "474", "497", "454", "477", "500"}
	scrfdStrides     = [3]int{8, 16, 32}
)

// Old ArcFace model files to clean up on init.
var arcfaceOldFiles = []string{"arcfaceresnet100-11-int8.onnx", "arcfaceresnet100-8.onnx"}

// ArcFace standard target landmarks for 112×112 aligned face input.
var arcfaceTargetLandmarks = [5][2]float64{
	{38.2946, 51.6963}, // left eye
	{73.5318, 51.5014}, // right eye
	{56.0252, 71.7366}, // nose
	{41.5493, 92.3655}, // left mouth corner
	{70.7299, 92.2041}, // right mouth corner
}

// cachedSession holds a reusable ONNX session with pre-allocated tensors.
// Data is copied into the input tensor's backing slice before each Run().
type cachedSession struct {
	session *ort.AdvancedSession
	input   *ort.Tensor[float32]
	outputs []*ort.Tensor[float32]
}

func (s *cachedSession) destroy() {
	if s == nil {
		return
	}
	if s.session != nil {
		s.session.Destroy()
	}
	if s.input != nil {
		s.input.Destroy()
	}
	for _, o := range s.outputs {
		o.Destroy()
	}
}

// OnnxBackend uses pigo for face detection, ArcFace ONNX for face embeddings,
// and MobileNetV2 ONNX for image classification tags.
type OnnxBackend struct {
	ModelsDir   string   // directory to store/load ONNX model files
	LibraryPath string   // path to libonnxruntime.so
	TagModel    TagModel // which tag model to use (mobilenet/joytag/wd14)

	once         sync.Once
	initErr      error
	pigo         *PigoBackend
	hasArcface   bool
	hasSCRFD     bool
	hasGenderage bool

	// Cached sessions for performance — avoid creating per-call.
	tagSession       *cachedSession // MobileNet, JoyTag, or WD14 depending on TagModel
	tagLabels        []string       // label list for the active tag model
	arcfaceSession   *cachedSession
	genderageSession *cachedSession
	scrfdSession     *cachedSession
}

func (b *OnnxBackend) Name() string { return "ONNX (local)" }

// initTagModel downloads the selected tag model and its label list.
func (b *OnnxBackend) initTagModel() error {
	switch b.TagModel {
	case TagModelJoyTag:
		if err := ensureModel(filepath.Join(b.ModelsDir, joytagFile), joytagURL); err != nil {
			return err
		}
		tagsPath := filepath.Join(b.ModelsDir, joytagTagsFile)
		if err := ensureModel(tagsPath, joytagTagsURL); err != nil {
			return err
		}
		data, err := os.ReadFile(tagsPath)
		if err != nil {
			return fmt.Errorf("read joytag tags: %w", err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				b.tagLabels = append(b.tagLabels, strings.ReplaceAll(line, "_", " "))
			}
		}
	case TagModelWD14:
		if err := ensureModel(filepath.Join(b.ModelsDir, wd14File), wd14URL); err != nil {
			return err
		}
		tagsPath := filepath.Join(b.ModelsDir, wd14TagsFile)
		if err := ensureModel(tagsPath, wd14TagsURL); err != nil {
			return err
		}
		data, err := os.ReadFile(tagsPath)
		if err != nil {
			return fmt.Errorf("read wd14 tags: %w", err)
		}
		// CSV format: tag_id,name,category,count
		for i, line := range strings.Split(string(data), "\n") {
			if i == 0 || strings.TrimSpace(line) == "" {
				continue // skip header
			}
			fields := strings.SplitN(line, ",", 3)
			if len(fields) >= 2 {
				b.tagLabels = append(b.tagLabels, strings.ReplaceAll(fields[1], "_", " "))
			}
		}
	default: // MobileNet — labels are built into imagenetLabels
		if err := ensureModel(filepath.Join(b.ModelsDir, mobilenetFile), mobilenetURL); err != nil {
			return err
		}
		b.tagLabels = imagenetLabels[:]
	}
	slog.Info("tag model initialized", "model", b.TagModel, "labels", len(b.tagLabels))
	return nil
}

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
			if err.Error() != "The onnxruntime has already been initialized" {
				b.initErr = fmt.Errorf("init onnxruntime: %w", err)
				return
			}
		}

		if err := os.MkdirAll(b.ModelsDir, 0o755); err != nil {
			b.initErr = fmt.Errorf("create models dir: %w", err)
			return
		}

		// Download and init the selected tag model.
		if err := b.initTagModel(); err != nil {
			b.initErr = fmt.Errorf("tag model: %w", err)
			return
		}

		// ArcFace is optional.
		arcfacePath := filepath.Join(b.ModelsDir, arcfaceFile)
		if err := ensureModel(arcfacePath, arcfaceURL); err != nil {
			slog.Warn("arcface model unavailable", "err", err)
		} else {
			b.hasArcface = true
			// Clean up old model files.
			for _, old := range arcfaceOldFiles {
				oldPath := filepath.Join(b.ModelsDir, old)
				if err := os.Remove(oldPath); err == nil {
					slog.Info("removed old arcface model", "path", oldPath)
				}
			}
		}

		// SCRFD face detection is optional — falls back to Pigo.
		scrfdPath := filepath.Join(b.ModelsDir, scrfdFile)
		if err := ensureModel(scrfdPath, scrfdURL); err != nil {
			slog.Warn("scrfd model unavailable, using pigo fallback", "err", err)
		} else {
			b.hasSCRFD = true
		}

		// Genderage model for age estimation (helps disambiguate similar-looking siblings).
		genderagePath := filepath.Join(b.ModelsDir, genderageFile)
		if err := ensureModel(genderagePath, genderageURL); err != nil {
			slog.Warn("genderage model unavailable, age estimation disabled", "err", err)
		} else {
			b.hasGenderage = true
		}

	})
}

func ensureModel(path, url string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	slog.Info("downloading model", "model", filepath.Base(path))
	resp, err := modelHTTP.Get(url)
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
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		if rmErr := os.Remove(tmp); rmErr != nil {
			slog.Warn("remove partial model download", "path", tmp, "err", rmErr)
		}
		return fmt.Errorf("download %s: %w", url, err)
	}
	return os.Rename(tmp, path)
}

// newCachedSession creates a reusable ONNX session with pre-allocated tensors.
func newCachedSession(modelPath string, inputNames, outputNames []string, inputShape ort.Shape, outputShapes []ort.Shape) (*cachedSession, error) {
	input, err := ort.NewEmptyTensor[float32](inputShape)
	if err != nil {
		return nil, fmt.Errorf("create input tensor: %w", err)
	}

	outputs := make([]*ort.Tensor[float32], len(outputShapes))
	outputValues := make([]ort.Value, len(outputShapes))
	for i, shape := range outputShapes {
		outputs[i], err = ort.NewEmptyTensor[float32](shape)
		if err != nil {
			input.Destroy()
			for j := range i {
				outputs[j].Destroy()
			}
			return nil, fmt.Errorf("create output tensor %d: %w", i, err)
		}
		outputValues[i] = outputs[i]
	}

	session, err := ort.NewAdvancedSession(modelPath,
		inputNames, outputNames,
		[]ort.Value{input}, outputValues, nil)
	if err != nil {
		input.Destroy()
		for _, o := range outputs {
			o.Destroy()
		}
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &cachedSession{session: session, input: input, outputs: outputs}, nil
}

// ensureSessions creates ONNX sessions on demand. Sessions are heavyweight
// (ONNX runtime allocates execution graphs and workspace buffers via C/malloc)
// so they are created when needed and released after the scan batch with Release().
func (b *OnnxBackend) ensureSessions() error {
	if b.tagSession != nil {
		return nil // already created
	}

	var err error
	switch b.TagModel {
	case TagModelJoyTag:
		b.tagSession, err = newCachedSession(
			filepath.Join(b.ModelsDir, joytagFile),
			[]string{"input"}, []string{"output"},
			ort.NewShape(1, 3, 448, 448),
			[]ort.Shape{ort.NewShape(1, int64(len(b.tagLabels)))})
	case TagModelWD14:
		b.tagSession, err = newCachedSession(
			filepath.Join(b.ModelsDir, wd14File),
			[]string{"input"}, []string{"output"},
			ort.NewShape(1, 448, 448, 3), // NHWC format
			[]ort.Shape{ort.NewShape(1, int64(len(b.tagLabels)))})
	default: // MobileNet
		b.tagSession, err = newCachedSession(
			filepath.Join(b.ModelsDir, mobilenetFile),
			[]string{"input"}, []string{"output"},
			ort.NewShape(1, 3, 224, 224),
			[]ort.Shape{ort.NewShape(1, 1000)})
	}
	if err != nil {
		return fmt.Errorf("tag session (%s): %w", b.TagModel, err)
	}

	if b.hasArcface {
		b.arcfaceSession, err = newCachedSession(
			filepath.Join(b.ModelsDir, arcfaceFile),
			[]string{"input.1"}, []string{"683"},
			ort.NewShape(1, 3, 112, 112),
			[]ort.Shape{ort.NewShape(1, 512)})
		if err != nil {
			return fmt.Errorf("arcface session: %w", err)
		}
	}

	// Genderage uses per-call sessions (not cached) because the cached
	// session pattern may not reliably update the C-side tensor data.

	if b.hasSCRFD {
		var gs [3]int64
		for i, stride := range scrfdStrides {
			g := int64(scrfdInputSize / stride)
			gs[i] = g * g * scrfdNumAnchors
		}
		outShapes := []ort.Shape{
			ort.NewShape(gs[0], 1), ort.NewShape(gs[1], 1), ort.NewShape(gs[2], 1),
			ort.NewShape(gs[0], 4), ort.NewShape(gs[1], 4), ort.NewShape(gs[2], 4),
			ort.NewShape(gs[0], 10), ort.NewShape(gs[1], 10), ort.NewShape(gs[2], 10),
		}
		b.scrfdSession, err = newCachedSession(
			filepath.Join(b.ModelsDir, scrfdFile),
			[]string{"input.1"}, scrfdOutputNames,
			ort.NewShape(1, 3, scrfdInputSize, scrfdInputSize),
			outShapes)
		if err != nil {
			return fmt.Errorf("scrfd session: %w", err)
		}
	}

	return nil
}

// Release frees ONNX runtime sessions and their C-allocated memory.
// Call after a scan batch completes to reclaim the ~GB of model workspace memory.
func (b *OnnxBackend) Release() {
	b.tagSession.destroy()
	b.tagSession = nil
	b.arcfaceSession.destroy()
	b.arcfaceSession = nil
	// genderageSession uses per-call sessions (not cached), no cleanup needed.
	b.scrfdSession.destroy()
	b.scrfdSession = nil
}

// InitErr returns any initialization error, triggering init if needed.
// Callers can check this before entering a loop to avoid per-image errors.
func (b *OnnxBackend) InitErr() error {
	b.init()
	return b.initErr
}

func (b *OnnxBackend) Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error) {
	b.init()
	if b.initErr != nil {
		return nil, b.initErr
	}
	if err := b.ensureSessions(); err != nil {
		return nil, err
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

	// Face detection: SCRFD (if available) or Pigo fallback.
	var detectedFaces []DetectedFace
	if b.hasSCRFD {
		faces, err := b.detectFacesSCRFD(img)
		if err != nil {
			slog.Warn("scrfd detect", "path", imagePath, "err", err)
		} else {
			detectedFaces = faces
		}
	}
	if detectedFaces == nil {
		// Pigo fallback.
		pigoResult, err := b.pigo.Analyze(ctx, imagePath)
		if err != nil {
			slog.Warn("pigo face detect", "path", imagePath, "err", err)
		} else {
			detectedFaces = pigoResult.Faces
		}
	}

	// Compute embeddings and age estimates for detected faces.
	bounds := img.Bounds()
	for i := range detectedFaces {
		face := &detectedFaces[i]
		face.EstimatedAge = -1

		// Skip tiny faces — upscaling produces garbage embeddings.
		facePixW := int(face.W * float64(bounds.Dx()))
		facePixH := int(face.H * float64(bounds.Dy()))
		if facePixW < 32 || facePixH < 32 {
			continue
		}

		// Compute alignment once (used by both ArcFace and genderage).
		var aligned image.Image
		if len(face.Landmarks) == 5 {
			aligned = alignFace(img, face.Landmarks)
		}

		if b.hasArcface {
			embedding, err := b.computeFaceEmbedding(aligned, img, face)
			if err != nil {
				slog.Warn("onnx arcface", "err", err)
			} else {
				face.Embedding = embedding
			}
		}

		// Age estimation needs a reasonably sized face crop — small faces
		// upscaled to 96×96 produce garbage age estimates.
		if b.hasGenderage && aligned != nil && facePixW >= 64 && facePixH >= 64 {
			age, err := b.estimateAge(aligned)
			if err != nil {
				slog.Warn("onnx genderage", "err", err)
			} else {
				face.EstimatedAge = age
			}
		}
	}
	result.Faces = detectedFaces

	return result, nil
}

// classifyImage runs MobileNetV2 on the image and returns top-5 tags.
func (b *OnnxBackend) classifyImage(img image.Image) ([]RecognizedTag, error) {
	s := b.tagSession

	// Preprocess based on model type.
	switch b.TagModel {
	case TagModelJoyTag:
		data := preprocessRaw01(img, 448, 448) // JoyTag: 0-1 range, NCHW
		copy(s.input.GetData(), data)
	case TagModelWD14:
		data := preprocessRaw01NHWC(img, 448, 448) // WD14: 0-1 range, NHWC
		copy(s.input.GetData(), data)
	default: // MobileNet
		data := preprocessImageNet(img, 224, 224)
		copy(s.input.GetData(), data)
	}

	if err := s.session.Run(); err != nil {
		return nil, fmt.Errorf("run tag model (%s): %w", b.TagModel, err)
	}

	output := s.outputs[0].GetData()

	// MobileNet uses softmax (single-label), JoyTag/WD14 use sigmoid (multi-label).
	if b.TagModel == TagModelMobileNet || b.TagModel == "" {
		return b.classifyMobileNet(output), nil
	}
	return b.classifyMultiLabel(output), nil
}

// classifyMobileNet processes softmax output from MobileNet (single best class).
func (b *OnnxBackend) classifyMobileNet(output []float32) []RecognizedTag {
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
		if indexed[i].index < len(b.tagLabels) {
			label = b.tagLabels[indexed[i].index]
		}
		tags = append(tags, RecognizedTag{
			Name:       label,
			Confidence: float64(indexed[i].prob),
			Type:       1,
		})
	}
	return tags
}

// classifyMultiLabel processes sigmoid output from JoyTag/WD14 (multiple tags).
func (b *OnnxBackend) classifyMultiLabel(output []float32) []RecognizedTag {
	// Apply sigmoid.
	probs := make([]float32, len(output))
	for i, v := range output {
		probs[i] = float32(1.0 / (1.0 + math.Exp(-float64(v))))
	}

	type indexedProb struct {
		index int
		prob  float32
	}
	var candidates []indexedProb
	for i, p := range probs {
		if p > 0.3 && i < len(b.tagLabels) { // pre-filter
			candidates = append(candidates, indexedProb{i, p})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].prob > candidates[j].prob
	})

	var tags []RecognizedTag
	for i := 0; i < 20 && i < len(candidates); i++ {
		tags = append(tags, RecognizedTag{
			Name:       b.tagLabels[candidates[i].index],
			Confidence: float64(candidates[i].prob),
			Type:       1,
		})
	}
	return tags
}

// preprocessRaw01 resizes to CHW float32 tensor with values in 0-1 range.
func preprocessRaw01(img image.Image, targetW, targetH int) []float32 {
	resized := resizeImage(img, targetW, targetH)
	tensor := make([]float32, 3*targetH*targetW)
	for y := range targetH {
		for x := range targetW {
			r, g, b, _ := resized.At(x, y).RGBA()
			tensor[0*targetH*targetW+y*targetW+x] = float32(r) / 65535.0
			tensor[1*targetH*targetW+y*targetW+x] = float32(g) / 65535.0
			tensor[2*targetH*targetW+y*targetW+x] = float32(b) / 65535.0
		}
	}
	return tensor
}

// preprocessRaw01NHWC resizes to NHWC float32 tensor with values in 0-1 range.
func preprocessRaw01NHWC(img image.Image, targetW, targetH int) []float32 {
	resized := resizeImage(img, targetW, targetH)
	tensor := make([]float32, targetH*targetW*3)
	for y := range targetH {
		for x := range targetW {
			r, g, b, _ := resized.At(x, y).RGBA()
			idx := (y*targetW + x) * 3
			tensor[idx] = float32(r) / 65535.0
			tensor[idx+1] = float32(g) / 65535.0
			tensor[idx+2] = float32(b) / 65535.0
		}
	}
	return tensor
}

// computeFaceEmbedding produces a 512-dim ArcFace embedding for a detected face.
// If aligned is non-nil (pre-computed from landmarks), it's used directly.
// Otherwise falls back to crop+resize from the full image.
func (b *OnnxBackend) computeFaceEmbedding(aligned, img image.Image, face *DetectedFace) ([]float32, error) {
	var data []float32

	if aligned != nil {
		data = preprocessInsightFace(aligned, 112, 112)
	} else {
		// Fallback: crop and resize (Pigo path, no landmarks).
		bounds := img.Bounds()
		imgW := float64(bounds.Dx())
		imgH := float64(bounds.Dy())

		x := int(face.X * imgW)
		y := int(face.Y * imgH)
		w := int(face.W * imgW)
		h := int(face.H * imgH)

		padW := w / 5
		padH := h / 5
		x = max(0, x-padW)
		y = max(0, y-padH)
		w = min(bounds.Dx()-x, w+2*padW)
		h = min(bounds.Dy()-y, h+2*padH)

		if w < 32 || h < 32 {
			return nil, fmt.Errorf("face crop too small: %dx%d", w, h)
		}

		faceCrop := cropImage(img, image.Rect(x, y, x+w, y+h))
		data = preprocessInsightFace(faceCrop, 112, 112)
	}

	s := b.arcfaceSession
	copy(s.input.GetData(), data)

	if err := s.session.Run(); err != nil {
		return nil, fmt.Errorf("run arcface: %w", err)
	}

	output := s.outputs[0].GetData()
	embedding := make([]float32, len(output))
	copy(embedding, output)
	l2normalize(embedding)

	return embedding, nil
}

// preprocessImageNet resizes an image and converts to CHW float32 tensor
// with ImageNet normalization (mean=[0.485,0.456,0.406], std=[0.229,0.224,0.225]).
// Used for MobileNet classification only.
func preprocessImageNet(img image.Image, targetW, targetH int) []float32 {
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

// preprocessInsightFace resizes an image and converts to CHW float32 tensor
// with InsightFace normalization: (pixel - 127.5) / 128.0.
// Used for ArcFace, genderage, and SCRFD models.
func preprocessInsightFace(img image.Image, targetW, targetH int) []float32 {
	resized := resizeImage(img, targetW, targetH)

	tensor := make([]float32, 3*targetH*targetW)
	for y := range targetH {
		for x := range targetW {
			r, g, b, _ := resized.At(x, y).RGBA()
			tensor[0*targetH*targetW+y*targetW+x] = (float32(r)/256.0 - 127.5) / 128.0
			tensor[1*targetH*targetW+y*targetW+x] = (float32(g)/256.0 - 127.5) / 128.0
			tensor[2*targetH*targetW+y*targetW+x] = (float32(b)/256.0 - 127.5) / 128.0
		}
	}
	return tensor
}

// preprocessRaw resizes an image and converts to CHW float32 tensor
// with raw 0-255 pixel values (no normalization). Used for the genderage model.
func preprocessRaw(img image.Image, targetW, targetH int) []float32 {
	resized := resizeImage(img, targetW, targetH)

	tensor := make([]float32, 3*targetH*targetW)
	for y := range targetH {
		for x := range targetW {
			r, g, b, _ := resized.At(x, y).RGBA()
			tensor[0*targetH*targetW+y*targetW+x] = float32(r) / 256.0
			tensor[1*targetH*targetW+y*targetW+x] = float32(g) / 256.0
			tensor[2*targetH*targetW+y*targetW+x] = float32(b) / 256.0
		}
	}
	return tensor
}

// resizeImage performs bilinear interpolation resize.
func resizeImage(img image.Image, w, h int) image.Image {
	bounds := img.Bounds()
	srcW := float64(bounds.Dx())
	srcH := float64(bounds.Dy())

	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			// Map to source coordinates (center of pixel).
			sx := (float64(x)+0.5)*srcW/float64(w) - 0.5
			sy := (float64(y)+0.5)*srcH/float64(h) - 0.5
			dst.Set(x, y, bilinearSample(img, sx, sy, bounds))
		}
	}
	return dst
}

// bilinearSample interpolates between 4 surrounding pixels at fractional source coordinates.
func bilinearSample(img image.Image, sx, sy float64, bounds image.Rectangle) color.Color {
	x0 := int(math.Floor(sx))
	y0 := int(math.Floor(sy))
	x1 := x0 + 1
	y1 := y0 + 1

	// Fractional parts.
	xf := sx - float64(x0)
	yf := sy - float64(y0)

	// Clamp to image bounds.
	clampX := func(x int) int {
		if x < 0 {
			return 0
		}
		if x >= bounds.Dx() {
			return bounds.Dx() - 1
		}
		return x
	}
	clampY := func(y int) int {
		if y < 0 {
			return 0
		}
		if y >= bounds.Dy() {
			return bounds.Dy() - 1
		}
		return y
	}

	// Sample 4 corners.
	r00, g00, b00, a00 := img.At(bounds.Min.X+clampX(x0), bounds.Min.Y+clampY(y0)).RGBA()
	r10, g10, b10, a10 := img.At(bounds.Min.X+clampX(x1), bounds.Min.Y+clampY(y0)).RGBA()
	r01, g01, b01, a01 := img.At(bounds.Min.X+clampX(x0), bounds.Min.Y+clampY(y1)).RGBA()
	r11, g11, b11, a11 := img.At(bounds.Min.X+clampX(x1), bounds.Min.Y+clampY(y1)).RGBA()

	// Bilinear interpolation.
	lerp := func(v00, v10, v01, v11 uint32) uint8 {
		top := float64(v00)*(1-xf) + float64(v10)*xf
		bot := float64(v01)*(1-xf) + float64(v11)*xf
		v := top*(1-yf) + bot*yf
		return uint8(v / 256)
	}

	return color.RGBA{
		R: lerp(r00, r10, r01, r11),
		G: lerp(g00, g10, g01, g11),
		B: lerp(b00, b10, b01, b11),
		A: lerp(a00, a10, a01, a11),
	}
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

// --- Age estimation ---

// estimateAge runs the genderage model on a 112×112 aligned face and returns estimated age.
// The model expects 96×96 input with raw 0-255 pixel values (NOT normalized).
func (b *OnnxBackend) estimateAge(aligned image.Image) (float64, error) {
	data := preprocessRaw(aligned, 96, 96)

	inputTensor, err := ort.NewTensor(ort.NewShape(1, 3, 96, 96), data)
	if err != nil {
		return -1, fmt.Errorf("create age input tensor: %w", err)
	}
	defer inputTensor.Destroy()

	outputTensor, err := ort.NewEmptyTensor[float32](ort.NewShape(1, 3))
	if err != nil {
		return -1, fmt.Errorf("create age output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	session, err := ort.NewAdvancedSession(
		filepath.Join(b.ModelsDir, genderageFile),
		[]string{"data"}, []string{"fc1"},
		[]ort.Value{inputTensor}, []ort.Value{outputTensor}, nil)
	if err != nil {
		return -1, fmt.Errorf("create genderage session: %w", err)
	}
	defer session.Destroy()

	if err := session.Run(); err != nil {
		return -1, fmt.Errorf("run genderage: %w", err)
	}

	output := outputTensor.GetData()
	// output[0] = female logit, output[1] = male logit, output[2] = age/100
	age := float64(output[2]) * 100
	if age < 0 || age > 120 {
		return -1, nil
	}
	return age, nil
}

// --- SCRFD face detection ---

// detectFacesSCRFD runs SCRFD on the image and returns detected faces with landmarks.
func (b *OnnxBackend) detectFacesSCRFD(img image.Image) ([]DetectedFace, error) {
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	// Letterbox to 640×640.
	letterboxed, scale, padX, padY := letterboxImage(img, scrfdInputSize, scrfdInputSize)

	// Preprocess and copy into cached session input.
	s := b.scrfdSession
	data := preprocessSCRFD(letterboxed)
	copy(s.input.GetData(), data)

	if err := s.session.Run(); err != nil {
		return nil, fmt.Errorf("run scrfd: %w", err)
	}

	// Decode detections from all strides.
	var dets []scrfdDetection

	for si, stride := range scrfdStrides {
		gs := scrfdInputSize / stride
		// Outputs are ordered: scores[0..2], boxes[3..5], kps[6..8].
		scoreData := s.outputs[si].GetData()
		boxData := s.outputs[si+3].GetData()
		kpsData := s.outputs[si+6].GetData()

		for y := range gs {
			for x := range gs {
				for a := range scrfdNumAnchors {
					idx := (y*gs+x)*scrfdNumAnchors + a
					score := float64(scoreData[idx])
					if score < scrfdScoreThreshold {
						continue
					}

					cx := float64(x*stride) + float64(stride)/2
					cy := float64(y*stride) + float64(stride)/2
					sf := float64(stride)

					d := scrfdDetection{score: score}
					d.x1 = cx - float64(boxData[idx*4])*sf
					d.y1 = cy - float64(boxData[idx*4+1])*sf
					d.x2 = cx + float64(boxData[idx*4+2])*sf
					d.y2 = cy + float64(boxData[idx*4+3])*sf

					for k := range 5 {
						d.landmarks[k] = [2]float64{
							cx + float64(kpsData[idx*10+k*2])*sf,
							cy + float64(kpsData[idx*10+k*2+1])*sf,
						}
					}
					dets = append(dets, d)
				}
			}
		}
	}

	// NMS.
	kept := nms(dets, scrfdNMSThreshold)

	// Convert to DetectedFace with normalized coordinates.
	var faces []DetectedFace
	for _, i := range kept {
		d := dets[i]
		// Map from letterbox coords to original image coords.
		x1 := (d.x1 - padX) / scale
		y1 := (d.y1 - padY) / scale
		x2 := (d.x2 - padX) / scale
		y2 := (d.y2 - padY) / scale

		face := DetectedFace{
			X:          x1 / imgW,
			Y:          y1 / imgH,
			W:          (x2 - x1) / imgW,
			H:          (y2 - y1) / imgH,
			Confidence: d.score,
		}

		landmarks := make([][2]float64, 5)
		for k := range 5 {
			landmarks[k] = [2]float64{
				((d.landmarks[k][0] - padX) / scale) / imgW,
				((d.landmarks[k][1] - padY) / scale) / imgH,
			}
		}
		face.Landmarks = landmarks
		faces = append(faces, face)
	}
	return faces, nil
}

// preprocessSCRFD converts an image to SCRFD's expected format: (pixel-127.5)/128.0, CHW.
func preprocessSCRFD(img image.Image) []float32 {
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	tensor := make([]float32, 3*h*w)
	for y := range h {
		for x := range w {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			tensor[0*h*w+y*w+x] = (float32(r)/256.0 - 127.5) / 128.0
			tensor[1*h*w+y*w+x] = (float32(g)/256.0 - 127.5) / 128.0
			tensor[2*h*w+y*w+x] = (float32(b)/256.0 - 127.5) / 128.0
		}
	}
	return tensor
}

// letterboxImage resizes an image to fit within targetW×targetH, preserving aspect ratio,
// and zero-pads to the exact target size. Returns the padded image and mapping parameters.
func letterboxImage(img image.Image, targetW, targetH int) (image.Image, float64, float64, float64) {
	bounds := img.Bounds()
	srcW := float64(bounds.Dx())
	srcH := float64(bounds.Dy())

	scale := math.Min(float64(targetW)/srcW, float64(targetH)/srcH)
	newW := int(srcW * scale)
	newH := int(srcH * scale)
	padX := float64(targetW-newW) / 2
	padY := float64(targetH-newH) / 2

	resized := resizeImage(img, newW, newH)

	dst := image.NewRGBA(image.Rect(0, 0, targetW, targetH))
	// Zero-fill (black) is the default for NewRGBA.
	ox := int(padX)
	oy := int(padY)
	for y := range newH {
		for x := range newW {
			dst.Set(ox+x, oy+y, resized.At(x, y))
		}
	}
	return dst, scale, padX, padY
}

// scrfdDetection holds a raw SCRFD detection before NMS.
type scrfdDetection struct {
	x1, y1, x2, y2 float64
	score          float64
	landmarks      [5][2]float64
}

// nms performs greedy non-maximum suppression on SCRFD detections.
func nms(dets []scrfdDetection, iouThreshold float64) []int {
	if len(dets) == 0 {
		return nil
	}

	// Sort by score descending.
	indices := make([]int, len(dets))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(i, j int) bool {
		return dets[indices[i]].score > dets[indices[j]].score
	})

	suppressed := make([]bool, len(dets))
	var kept []int
	for _, i := range indices {
		if suppressed[i] {
			continue
		}
		kept = append(kept, i)
		for _, j := range indices {
			if suppressed[j] || i == j {
				continue
			}
			if iou(dets[i], dets[j]) > iouThreshold {
				suppressed[j] = true
			}
		}
	}
	return kept
}

func iou(a, b scrfdDetection) float64 {
	x1 := math.Max(a.x1, b.x1)
	y1 := math.Max(a.y1, b.y1)
	x2 := math.Min(a.x2, b.x2)
	y2 := math.Min(a.y2, b.y2)
	inter := math.Max(0, x2-x1) * math.Max(0, y2-y1)
	areaA := (a.x2 - a.x1) * (a.y2 - a.y1)
	areaB := (b.x2 - b.x1) * (b.y2 - b.y1)
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// --- Face alignment ---

// alignFace warps a face from the full image to a canonical 112×112 aligned crop
// using the 5 facial landmarks detected by SCRFD.
func alignFace(img image.Image, landmarks [][2]float64) image.Image {
	bounds := img.Bounds()
	imgW := float64(bounds.Dx())
	imgH := float64(bounds.Dy())

	// Convert normalized landmarks to pixel coordinates.
	src := make([][2]float64, 5)
	for i, lm := range landmarks {
		src[i] = [2]float64{lm[0] * imgW, lm[1] * imgH}
	}

	// Compute similarity transform from source → ArcFace target landmarks.
	M := estimateSimilarityTransform(src[:], arcfaceTargetLandmarks[:])

	slog.Debug("align transform",
		"src0", fmt.Sprintf("(%.1f,%.1f)", src[0][0], src[0][1]),
		"src1", fmt.Sprintf("(%.1f,%.1f)", src[1][0], src[1][1]),
		"M", fmt.Sprintf("[[%.6f,%.6f,%.1f],[%.6f,%.6f,%.1f]]",
			M[0][0], M[0][1], M[0][2], M[1][0], M[1][1], M[1][2]),
	)

	// Warp image to produce aligned 112×112 face.
	return warpAffine(img, M, 112, 112)
}

// estimateSimilarityTransform computes a 2×3 affine matrix for a similarity transform
// (rotation + uniform scale + translation) mapping src points to dst points.
func estimateSimilarityTransform(src, dst [][2]float64) [2][3]float64 {
	n := len(src)
	// Solve for [a, b, tx, ty] where:
	//   dst_x = a*src_x - b*src_y + tx
	//   dst_y = b*src_x + a*src_y + ty
	// Using normal equations: (A^T A) x = A^T b.
	var ata [4][4]float64
	var atb [4]float64

	for i := range n {
		sx, sy := src[i][0], src[i][1]
		dx, dy := dst[i][0], dst[i][1]
		row1 := [4]float64{sx, -sy, 1, 0}
		row2 := [4]float64{sy, sx, 0, 1}
		for j := range 4 {
			for k := range 4 {
				ata[j][k] += row1[j]*row1[k] + row2[j]*row2[k]
			}
			atb[j] += row1[j]*dx + row2[j]*dy
		}
	}

	x := solve4x4(ata, atb)
	return [2][3]float64{
		{x[0], -x[1], x[2]},
		{x[1], x[0], x[3]},
	}
}

// solve4x4 solves a 4×4 linear system Ax = b using Gaussian elimination with partial pivoting.
func solve4x4(A [4][4]float64, b [4]float64) [4]float64 {
	// Augmented matrix.
	var m [4][5]float64
	for i := range 4 {
		for j := range 4 {
			m[i][j] = A[i][j]
		}
		m[i][4] = b[i]
	}

	// Forward elimination with partial pivoting.
	for col := range 4 {
		// Find pivot.
		maxRow := col
		for row := col + 1; row < 4; row++ {
			if math.Abs(m[row][col]) > math.Abs(m[maxRow][col]) {
				maxRow = row
			}
		}
		m[col], m[maxRow] = m[maxRow], m[col]

		pivot := m[col][col]
		if math.Abs(pivot) < 1e-12 {
			continue
		}
		for row := col + 1; row < 4; row++ {
			factor := m[row][col] / pivot
			for j := col; j < 5; j++ {
				m[row][j] -= factor * m[col][j]
			}
		}
	}

	// Back substitution.
	var x [4]float64
	for i := 3; i >= 0; i-- {
		x[i] = m[i][4]
		for j := i + 1; j < 4; j++ {
			x[i] -= m[i][j] * x[j]
		}
		if math.Abs(m[i][i]) > 1e-12 {
			x[i] /= m[i][i]
		}
	}
	return x
}

// warpAffine applies an affine transform M (mapping src→dst) to produce an output image.
// Uses bilinear interpolation via the inverse transform.
func warpAffine(img image.Image, M [2][3]float64, outW, outH int) image.Image {
	// Invert the 2×3 similarity transform.
	// For M = [[a, -b, tx], [b, a, ty]], det = a² + b²
	a, b := M[0][0], M[1][0]
	tx, ty := M[0][2], M[1][2]
	det := a*a + b*b
	if det < 1e-12 {
		return image.NewRGBA(image.Rect(0, 0, outW, outH))
	}
	// Inverse: [[a/det, b/det, -(a*tx+b*ty)/det], [-b/det, a/det, (b*tx-a*ty)/det]]
	invA := a / det
	invB := b / det
	invTx := -(a*tx + b*ty) / det
	invTy := (b*tx - a*ty) / det

	bounds := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, outW, outH))
	for y := range outH {
		for x := range outW {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			sx := invA*fx + invB*fy + invTx
			sy := -invB*fx + invA*fy + invTy
			dst.Set(x, y, bilinearSample(img, sx-0.5, sy-0.5, bounds))
		}
	}
	return dst
}
