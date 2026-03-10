package gallery

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// RecognizedTag is a tag produced by image recognition.
type RecognizedTag struct {
	Name       string
	Confidence float64
	Type       int // 1=object, 2=scene
}

// DetectedFace is a face detected in an image.
type DetectedFace struct {
	X, Y, W, H float64   // normalized bounding box 0.0-1.0
	Embedding  []float32 // face embedding vector
	Confidence float64
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

// getRecognitionBackend returns the configured recognition backend from system settings, or nil.
func (p *Plugin) getRecognitionBackend(db *sql.DB) RecognitionBackend {
	backend := GetSetting(db, "recognition_backend", "none")
	if backend == "none" || GetSetting(db, "recognition_enabled", "false") != "true" {
		return nil
	}

	url := GetSetting(db, "recognition_url", "")
	apiKey := GetSetting(db, "recognition_api_key", "")
	model := GetSetting(db, "recognition_model", "")

	switch backend {
	case "ollama":
		b := &OllamaBackend{URL: url, Model: model}
		if b.Available() {
			return b
		}
	case "openai":
		b := &OpenAIBackend{URL: url, APIKey: apiKey, Model: model}
		if b.Available() {
			return b
		}
	case "pigo":
		return &PigoBackend{}
	case "onnx":
		modelsDir := GetSetting(db, "onnx_models_dir", "")
		if modelsDir == "" {
			modelsDir = p.ctx.DataDir() + "/onnx_models"
		}
		b := &OnnxBackend{
			ModelsDir:   modelsDir,
			LibraryPath: GetSetting(db, "onnx_library_path", ""),
		}
		if b.Available() {
			return b
		}
	}

	return nil
}

// runRecognition processes pending media through the recognition backend.
func (p *Plugin) runRecognition(ctx context.Context, db *sql.DB, username string) error {
	sysDB, err := p.SystemDB()
	if err != nil {
		return nil
	}
	backend := p.getRecognitionBackend(sysDB)
	if backend == nil {
		return nil
	}

	rows, err := db.Query(`SELECT ` + mediaCols + ` FROM media
		WHERE recognition_state = 0 AND mime_type LIKE 'image/%'
		LIMIT 50`)
	if err != nil {
		return err
	}
	defer rows.Close()

	items, err := scanMediaRows(rows)
	if err != nil {
		return err
	}

	filesDir := p.ctx.UserFilesDir(username)
	tagMinConf := 0.5
	if v := GetSetting(sysDB, "tag_min_confidence", ""); v != "" {
		fmt.Sscanf(v, "%f", &tagMinConf)
	}
	faceMinConf := 0.7
	if v := GetSetting(sysDB, "face_min_confidence", ""); v != "" {
		fmt.Sscanf(v, "%f", &faceMinConf)
	}

	for i := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		m := &items[i]
		imagePath := filesDir + "/" + m.FilePath

		result, err := backend.Analyze(ctx, imagePath)
		now := time.Now().Unix()
		if err != nil {
			p.log.Error("recognize", "path", m.FilePath, "err", err)
			db.Exec(`UPDATE media SET recognition_state = 2, recognition_at = ? WHERE id = ?`, now, m.ID)
			continue
		}

		// Store tags.
		for _, tag := range result.Tags {
			if tag.Confidence < tagMinConf {
				continue
			}
			tagID, err := UpsertTag(db, tag.Name, tag.Type)
			if err != nil {
				continue
			}
			AddMediaTag(db, m.ID, tagID, tag.Confidence)
		}

		// Store faces.
		if GetSetting(sysDB, "face_detection_enabled", "true") == "true" {
			for _, face := range result.Faces {
				if face.Confidence < faceMinConf {
					continue
				}
				storeFace(db, m.ID, &face)
			}
		}

		db.Exec(`UPDATE media SET recognition_state = 1, recognition_at = ? WHERE id = ?`, now, m.ID)
	}

	return nil
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

	db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		mediaID, x, y, w, h, embeddingBlob, face.Confidence)
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
