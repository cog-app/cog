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
			global, err := p.ctx.GlobalStorage()
			if err != nil {
				return nil, err
			}
			if modelsDir, err = global.LocalPath("onnx_models"); err != nil {
				return nil, err
			}
		}
		b := &OnnxBackend{
			ModelsDir:   modelsDir,
			LibraryPath: SettingOnnxLibraryPath.MustGet(db),
			TagModel:    SettingTagModel.MustGet(db),
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

		imagePath, failure := p.resolveLocalPath(username, m.FilePath)
		now := p.ctx.Now().Unix()

		var tags []RecognizedTag
		var faces []DetectedFace
		switch {
		case failure != nil:
			// Recorded below as this image's failure, like a backend's.
		case shared:
			// Both features share a backend — call Analyze once.
			result, err := tagBackend.Analyze(ctx, imagePath)
			if err != nil {
				failure = err
			} else {
				tags, faces = result.Tags, result.Faces
			}
		default:
			// Different backends — call each independently. One failing
			// still keeps what the other found.
			if tagBackend != nil {
				if result, err := tagBackend.Analyze(ctx, imagePath); err != nil {
					failure = err
				} else {
					tags = result.Tags
				}
			}
			if faceBackend != nil {
				if result, err := faceBackend.Analyze(ctx, imagePath); err != nil {
					failure = err
				} else {
					faces = result.Faces
				}
			}
		}
		if failure != nil {
			p.log.Error("recognize", "path", m.FilePath, "err", failure)
		}

		if err := storeRecognition(db, m.ID, tags, faces, tagMinConf, faceMinConf, now, failure); err != nil {
			return fmt.Errorf("store recognition for %s: %w", m.FilePath, err)
		}
		if failure != nil {
			continue
		}
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
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
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
	} else if _, err := tx.Exec(`UPDATE media SET recognition_state = 1, recognition_at = ? WHERE id = ?`, now, mediaID); err != nil {
		return fmt.Errorf("mark media %d recognized: %w", mediaID, err)
	}
	return tx.Commit()
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

	if _, err := db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, estimated_age)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		mediaID, x, y, w, h, embeddingBlob, face.Confidence, face.EstimatedAge); err != nil {
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
