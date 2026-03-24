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
		modelsDir := SettingOnnxModelsDir.MustGet(db)
		if modelsDir == "" {
			modelsDir = p.ctx.DataDir() + "/onnx_models"
		}
		b := &OnnxBackend{
			ModelsDir:   modelsDir,
			LibraryPath: SettingOnnxLibraryPath.MustGet(db),
		}
		if !b.Available() {
			return nil, fmt.Errorf("onnx backend not available: libonnxruntime.so not found (library_path=%q)", b.LibraryPath)
		}
		return b, nil
	default:
		return nil, fmt.Errorf("unknown recognition backend: %q", backendType)
	}
}

// runRecognition processes pending media through tag and face backends.
// Each feature has its own backend setting; if both use the same backend type,
// Analyze is called once and results are split.
func (p *Plugin) runRecognition(ctx context.Context, db *sql.DB, username string) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return fmt.Errorf("system db: %w", err)
	}

	tagBackendType := SettingTagBackend.MustGet(sysDB)
	faceBackendType := SettingFaceBackend.MustGet(sysDB)
	if tagBackendType == BackendNone && faceBackendType == BackendNone {
		return nil
	}

	// Resolve backends. If both use the same type, share the instance.
	var tagBackend, faceBackend RecognitionBackend
	if tagBackendType != BackendNone {
		tagBackend, err = p.resolveBackend(sysDB, tagBackendType)
		if err != nil {
			return fmt.Errorf("tag backend: %w", err)
		}
	}
	if faceBackendType != BackendNone {
		if faceBackendType == tagBackendType && tagBackend != nil {
			faceBackend = tagBackend // share instance
		} else {
			faceBackend, err = p.resolveBackend(sysDB, faceBackendType)
			if err != nil {
				return fmt.Errorf("face backend: %w", err)
			}
		}
	}

	// Eagerly check for init errors.
	for _, b := range []RecognitionBackend{tagBackend, faceBackend} {
		if b == nil {
			continue
		}
		if ic, ok := b.(interface{ InitErr() error }); ok {
			if err := ic.InitErr(); err != nil {
				return fmt.Errorf("recognition backend init: %w", err)
			}
		}
	}

	// Register active backends so ReleaseMemory RPC can reach them under memory pressure.
	// Release sessions after this pass to free C-allocated memory.
	var backends []RecognitionBackend
	for _, b := range []RecognitionBackend{tagBackend, faceBackend} {
		if b == nil {
			continue
		}
		backends = append(backends, b)
	}
	p.activeBackendsMu.Lock()
	p.activeBackends = backends
	p.activeBackendsMu.Unlock()
	defer func() {
		for _, b := range backends {
			if rc, ok := b.(interface{ Release() }); ok {
				rc.Release()
			}
		}
		p.activeBackendsMu.Lock()
		p.activeBackends = nil
		p.activeBackendsMu.Unlock()
	}()

	p.log.Info("recognition started", "user", username,
		"tag_backend", tagBackendType, "face_backend", faceBackendType)

	tagMinConf := SettingTagMinConfidence.MustGet(sysDB)
	faceMinConf := SettingFaceMinConfidence.MustGet(sysDB)
	shared := tagBackend != nil && tagBackend == faceBackend

	var processed int
	for m, err := range pendingRecognition(db) {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		imagePath := p.resolveLocalPath(username, m.FilePath)
		now := p.ctx.Now().Unix()

		if shared {
			// Both features share a backend — call Analyze once.
			result, analyzeErr := tagBackend.Analyze(ctx, imagePath)
			if analyzeErr != nil {
				p.log.Error("recognize", "path", m.FilePath, "err", analyzeErr)
				db.Exec(`UPDATE media SET recognition_state = 2, recognition_at = ?, recognition_error = ? WHERE id = ?`, now, analyzeErr.Error(), m.ID)
				continue
			}
			storeTags(db, m.ID, result.Tags, tagMinConf)
			storeFaces(db, m.ID, result.Faces, faceMinConf)
		} else {
			// Different backends — call each independently.
			var anyErr error
			if tagBackend != nil {
				result, err := tagBackend.Analyze(ctx, imagePath)
				if err != nil {
					anyErr = err
				} else {
					storeTags(db, m.ID, result.Tags, tagMinConf)
				}
			}
			if faceBackend != nil {
				result, err := faceBackend.Analyze(ctx, imagePath)
				if err != nil {
					anyErr = err
				} else {
					storeFaces(db, m.ID, result.Faces, faceMinConf)
				}
			}
			if anyErr != nil {
				p.log.Error("recognize", "path", m.FilePath, "err", anyErr)
				db.Exec(`UPDATE media SET recognition_state = 2, recognition_at = ?, recognition_error = ? WHERE id = ?`, now, anyErr.Error(), m.ID)
				continue
			}
		}

		db.Exec(`UPDATE media SET recognition_state = 1, recognition_at = ? WHERE id = ?`, now, m.ID)
		processed++
		if processed%50 == 0 {
			p.log.Info("recognition progress", "user", username, "processed", processed)
		}
	}
	if processed > 0 {
		p.log.Info("recognition complete", "user", username, "processed", processed)
	}
	return nil
}

func storeTags(db *sql.DB, mediaID int64, tags []RecognizedTag, minConf float64) {
	for _, tag := range tags {
		if tag.Confidence < minConf {
			continue
		}
		tagID, err := UpsertTag(db, tag.Name, tag.Type)
		if err != nil {
			continue
		}
		AddMediaTag(db, mediaID, tagID, tag.Confidence)
	}
}

func storeFaces(db *sql.DB, mediaID int64, faces []DetectedFace, minConf float64) {
	for i := range faces {
		if faces[i].Confidence < minConf {
			continue
		}
		storeFace(db, mediaID, &faces[i])
	}
}

// storeFace inserts a detected face into the database.
func storeFace(db *sql.DB, mediaID int64, face *DetectedFace) {
	// Clamp bounding box.
	x := clamp(face.X, 0, 1)
	y := clamp(face.Y, 0, 1)
	w := clamp(face.W, 0, 1-x)
	h := clamp(face.H, 0, 1-y)

	var embeddingBlob []byte
	if len(face.Embedding) > 0 {
		embeddingBlob = float32SliceToBytes(face.Embedding)
	}

	db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, estimated_age)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mediaID, x, y, w, h, embeddingBlob, face.Confidence, face.EstimatedAge)
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
