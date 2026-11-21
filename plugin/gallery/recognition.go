package gallery

import (
	"context"
	"database/sql"
	"fmt"
)

// RecognizedTag is a tag produced by image recognition.
type RecognizedTag struct {
	Name       string
	Confidence float64
	Type       int // 1=object, 2=scene
}

// DetectedFace is a face detected in an image.
type DetectedFace struct {
	X, Y, W, H   float64      // normalized bounding box 0.0-1.0
	Landmarks    [][2]float64 // 5 facial landmarks [(x,y)...], normalized 0-1; nil if unavailable
	Embedding    []float32    // face embedding vector
	EstimatedAge float64      // estimated age in years, -1 if not available
	Confidence   float64
	Quality      float64 // see faceQuality; 0 leaves it to be worked out from the box
}

// RecognitionResult holds the output of image analysis.
type RecognitionResult struct {
	Tags  []RecognizedTag
	Faces []DetectedFace
}

// RecognitionBackend is the interface for image recognition services.
type RecognitionBackend interface {
	// Analyze processes an image and returns recognition results.
	Analyze(ctx context.Context, imagePath string) (*RecognitionResult, error)
	// Name returns a human-readable backend name.
	Name() string
	// Available reports whether this backend is configured and ready.
	Available() bool
}

// resolveBackend creates a RecognitionBackend for the given backend type.
func (p *Plugin) resolveBackend(db *sql.DB, backendType RecognitionBackendType) (RecognitionBackend, error) {
	if backendType == BackendNone {
		return nil, nil
	}
	url := SettingRecognitionURL.MustGet(db)
	apiKey := SettingRecognitionAPIKey.MustGet(db)
	model := SettingRecognitionModel.MustGet(db)

	switch backendType {
	case BackendOllama:
		b := &OllamaBackend{URL: url, Model: model}
		if !b.Available() {
			return nil, fmt.Errorf("ollama backend not available (url=%q, model=%q)", url, model)
		}
		return b, nil
	case BackendOpenAI:
		b := &OpenAIBackend{URL: url, APIKey: apiKey, Model: model}
		if !b.Available() {
			return nil, fmt.Errorf("openai backend not available (url=%q)", url)
		}
		return b, nil
	case BackendPigo:
		return &PigoBackend{}, nil
	case BackendOnnx:
		modelsDir, err := p.onnxModelsDir(db)
		if err != nil {
			return nil, err
		}
		b := &OnnxBackend{
			ModelsDir:   modelsDir,
			LibraryPath: SettingOnnxLibraryPath.MustGet(db),
			TagModel:    SettingTagModel.MustGet(db),
			Threads:     SettingRecognitionThreads.MustGet(db),
			Device:      onnxDevices()[0],
		}
		if !b.Available() {
			return nil, fmt.Errorf("onnx backend not available: libonnxruntime.so not found (library_path=%q)", b.LibraryPath)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("unknown recognition backend: %q", backendType)
	}
}

func (p *Plugin) onnxModelsDir(sysDB *sql.DB) (string, error) {
	if dir := SettingOnnxModelsDir.MustGet(sysDB); dir != "" {
		return dir, nil
	}
	global, err := p.ctx.GlobalStorage()
	if err != nil {
		return "", err
	}
	return global.LocalPath("onnx_models")
}

// execQuerier is what the recognition writes need: a *sql.DB or a *sql.Tx.
type execQuerier interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// storeRecognition stores what recognition found for a media item and marks
// it done — or, with failure, failed, so the pass does not retry it — in one
// transaction, so a crash part way neither loses the state nor stores the
// tags and faces twice on the next pass.
func storeRecognition(db *sql.DB, mediaID int64, tags []RecognizedTag, faces []DetectedFace, tagMinConf, faceMinConf float64, now int64, failure error) error {
	return inTx(db, func(tx *sql.Tx) error {
		return storeRecognitionTx(tx, mediaID, tags, faces, tagMinConf, faceMinConf, now, failure)
	})
}

// storeRecognitionTx is storeRecognition within a caller's transaction.
func storeRecognitionTx(tx *sql.Tx, mediaID int64, tags []RecognizedTag, faces []DetectedFace, tagMinConf, faceMinConf float64, now int64, failure error) error {
	// What an earlier pass found belongs to the file as it was; a tag
	// someone added by hand (confidence 1) stays.
	if _, err := tx.Exec(`DELETE FROM faces WHERE media_id = ?`, mediaID); err != nil {
		return fmt.Errorf("clear faces of media %d: %w", mediaID, err)
	}
	if _, err := tx.Exec(`DELETE FROM media_tags WHERE media_id = ? AND confidence < 1`, mediaID); err != nil {
		return fmt.Errorf("clear tags of media %d: %w", mediaID, err)
	}
	if err := storeTags(tx, mediaID, tags, tagMinConf); err != nil {
		return err
	}
	if err := storeFaces(tx, mediaID, faces, faceMinConf); err != nil {
		return err
	}
	if failure != nil {
		if _, err := tx.Exec(`UPDATE media SET recognition_state = 2, recognition_at = ?, recognition_error = ? WHERE id = ?`,
			now, failure.Error(), mediaID); err != nil {
			return fmt.Errorf("mark media %d failed: %w", mediaID, err)
		}
	} else if _, err := tx.Exec(`UPDATE media SET recognition_state = 1, recognition_at = ?, recognition_error = '' WHERE id = ?`, now, mediaID); err != nil {
		return fmt.Errorf("mark media %d recognized: %w", mediaID, err)
	}
	return nil
}

func storeTags(db execQuerier, mediaID int64, tags []RecognizedTag, minConf float64) error {
	for _, tag := range tags {
		if tag.Confidence < minConf {
			continue
		}
		tagID, err := UpsertTag(db, tag.Name, tag.Type)
		if err != nil {
			return fmt.Errorf("store tag %q: %w", tag.Name, err)
		}
		if err := AddMediaTag(db, mediaID, tagID, tag.Confidence); err != nil {
			return fmt.Errorf("tag media %d: %w", mediaID, err)
		}
	}
	return nil
}

func storeFaces(db execQuerier, mediaID int64, faces []DetectedFace, minConf float64) error {
	for i := range faces {
		if faces[i].Confidence < minConf {
			continue
		}
		if err := storeFace(db, mediaID, &faces[i]); err != nil {
			return err
		}
	}
	return nil
}

// storeFace inserts a detected face into the database.
func storeFace(db execQuerier, mediaID int64, face *DetectedFace) error {
	// Clamp bounding box.
	x := clamp(face.X, 0, 1)
	y := clamp(face.Y, 0, 1)
	w := clamp(face.W, 0, 1-x)
	h := clamp(face.H, 0, 1-y)

	var embeddingBlob []byte
	if len(face.Embedding) > 0 {
		embeddingBlob = float32SliceToBytes(face.Embedding)
	}

	quality := face.Quality
	if quality <= 0 {
		quality = -1
	}
	if _, err := db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, estimated_age, quality)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mediaID, x, y, w, h, embeddingBlob, face.Confidence, face.EstimatedAge, quality); err != nil {
		return fmt.Errorf("store face on media %d: %w", mediaID, err)
	}
	return nil
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
