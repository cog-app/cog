package gallery

import (
	"database/sql"
	"encoding/binary"
	"log/slog"
	"math"
	"strconv"
)

const (
	defaultClusterThreshold = 0.35 // cosine distance threshold for face clustering
)

// clusterFaces assigns unassigned faces to existing people or creates new clusters.
// If sysDB is non-nil, the threshold is read from system settings.
func clusterFaces(db *sql.DB, sysDB *sql.DB) error {
	threshold := defaultClusterThreshold
	if sysDB != nil {
		if v, err := strconv.ParseFloat(GetSetting(sysDB, "cluster_threshold", "0.35"), 64); err == nil && v > 0 {
			threshold = v
		}
	}

	// Load all existing people centroids.
	centroids, err := loadPersonCentroids(db)
	if err != nil {
		return err
	}

	// Get unassigned faces with embeddings.
	rows, err := db.Query(`SELECT id, media_id, embedding FROM faces
		WHERE person_id IS NULL AND embedding IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type unassignedFace struct {
		id        int64
		mediaID   int64
		embedding []float32
	}
	var faces []unassignedFace
	for rows.Next() {
		var f unassignedFace
		var blob []byte
		if err := rows.Scan(&f.id, &f.mediaID, &blob); err != nil {
			continue
		}
		f.embedding = bytesToFloat32Slice(blob)
		if len(f.embedding) > 0 {
			faces = append(faces, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, face := range faces {
		bestPersonID := int64(0)
		bestDist := threshold

		// Compare against existing centroids.
		for personID, centroid := range centroids {
			dist := cosineDistance(face.embedding, centroid)
			if dist < bestDist {
				bestDist = dist
				bestPersonID = personID
			}
		}

		if bestPersonID > 0 {
			// Assign to existing person.
			db.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, bestPersonID, face.id)
		} else {
			// Create new person cluster.
			res, err := db.Exec(`INSERT INTO people (name) VALUES ('')`)
			if err != nil {
				slog.Error("create person cluster", "err", err)
				continue
			}
			personID, _ := res.LastInsertId()
			db.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, personID, face.id)
			db.Exec(`UPDATE people SET representative_face_id = ? WHERE id = ?`, face.id, personID)
			centroids[personID] = face.embedding
		}
	}

	return nil
}

// loadPersonCentroids loads the representative face embedding for each person.
func loadPersonCentroids(db *sql.DB) (map[int64][]float32, error) {
	rows, err := db.Query(`SELECT p.id, f.embedding FROM people p
		JOIN faces f ON f.id = p.representative_face_id
		WHERE f.embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	centroids := make(map[int64][]float32)
	for rows.Next() {
		var personID int64
		var blob []byte
		if err := rows.Scan(&personID, &blob); err != nil {
			continue
		}
		emb := bytesToFloat32Slice(blob)
		if len(emb) > 0 {
			centroids[personID] = emb
		}
	}
	return centroids, rows.Err()
}

// cosineDistance computes the cosine distance between two vectors (1 - cosine similarity).
func cosineDistance(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1.0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 1.0
	}
	return 1 - dot/denom
}

// float32SliceToBytes converts a float32 slice to a byte slice (little-endian).
func float32SliceToBytes(f []float32) []byte {
	buf := make([]byte, len(f)*4)
	for i, v := range f {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// bytesToFloat32Slice converts a byte slice to a float32 slice (little-endian).
func bytesToFloat32Slice(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	f := make([]float32, len(b)/4)
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return f
}
