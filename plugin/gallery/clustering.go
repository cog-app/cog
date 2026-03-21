package gallery

import (
	"database/sql"
	"encoding/binary"
	"log/slog"
	"math"
	"strconv"
	"time"
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

	// Load recent face embeddings per person for chain matching (bridge effect).
	recentFaces, err := loadRecentFaces(db, 5)
	if err != nil {
		return err
	}

	// Load inferred birth years for age-based disambiguation.
	birthYears, err := loadPersonBirthYears(db)
	if err != nil {
		return err
	}

	// Get unassigned faces with embeddings, age estimates, and photo dates.
	rows, err := db.Query(`SELECT f.id, f.media_id, f.embedding, f.estimated_age,
		COALESCE(m.date_taken, m.date_indexed)
		FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NULL AND f.embedding IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type unassignedFace struct {
		id           int64
		mediaID      int64
		embedding    []float32
		estimatedAge float64
		photoDate    int64
	}
	var faces []unassignedFace
	for rows.Next() {
		var f unassignedFace
		var blob []byte
		if err := rows.Scan(&f.id, &f.mediaID, &blob, &f.estimatedAge, &f.photoDate); err != nil {
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

		// Compare against existing centroids with age penalty.
		for personID, centroid := range centroids {
			dist := cosineDistance(face.embedding, centroid)
			dist += agePenalty(face.estimatedAge, face.photoDate, birthYears[personID])
			if dist < bestDist {
				bestDist = dist
				bestPersonID = personID
			}
		}

		// Chain matching: if centroid didn't match, try recent faces.
		if bestPersonID == 0 {
			for personID, recent := range recentFaces {
				for _, emb := range recent {
					dist := cosineDistance(face.embedding, emb)
					dist += agePenalty(face.estimatedAge, face.photoDate, birthYears[personID])
					if dist < bestDist {
						bestDist = dist
						bestPersonID = personID
					}
				}
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

	// Faces without embeddings (e.g. Pigo-only, no ArcFace) can't be clustered.
	// Create a separate person for each so the user can merge manually.
	noEmbRows, err := db.Query(`SELECT id FROM faces
		WHERE person_id IS NULL AND embedding IS NULL`)
	if err != nil {
		return err
	}
	defer noEmbRows.Close()

	for noEmbRows.Next() {
		var faceID int64
		if err := noEmbRows.Scan(&faceID); err != nil {
			continue
		}
		res, err := db.Exec(`INSERT INTO people (name) VALUES ('')`)
		if err != nil {
			slog.Error("create person for embeddingless face", "err", err)
			continue
		}
		personID, _ := res.LastInsertId()
		db.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, personID, faceID)
		db.Exec(`UPDATE people SET representative_face_id = ? WHERE id = ?`, faceID, personID)
	}

	return nil
}

// recencyHalfLife controls how quickly older photos lose weight in centroid
// computation. Photos from this many seconds ago contribute half as much as
// today's photos. 5 years handles children aging gracefully.
const recencyHalfLife = 5 * 365.25 * 24 * 3600 // seconds

// loadPersonCentroids computes the weighted mean embedding across all faces for
// each person. Weights combine crop quality (larger faces are better) and
// recency (newer photos are weighted higher to track aging faces).
func loadPersonCentroids(db *sql.DB) (map[int64][]float32, error) {
	now := float64(time.Now().Unix())
	rows, err := db.Query(`SELECT f.person_id, f.embedding, f.w * m.width * f.h * m.height,
		COALESCE(m.date_taken, m.date_indexed)
		FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NOT NULL AND f.embedding IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Accumulate weighted sum per person.
	type accumulator struct {
		sum         []float64
		totalWeight float64
	}
	accum := make(map[int64]*accumulator)

	for rows.Next() {
		var personID int64
		var blob []byte
		var cropArea float64
		var dateTaken int64
		if err := rows.Scan(&personID, &blob, &cropArea, &dateTaken); err != nil {
			continue
		}
		emb := bytesToFloat32Slice(blob)
		if len(emb) == 0 {
			continue
		}
		// Combine crop quality with recency. Recency uses half-life decay
		// so the centroid tracks the person's current appearance.
		qualityWeight := math.Sqrt(math.Max(cropArea, 1))
		age := now - float64(dateTaken)
		recency := math.Max(math.Exp2(-age/recencyHalfLife), 0.1)
		weight := qualityWeight * recency

		a, ok := accum[personID]
		if !ok {
			a = &accumulator{sum: make([]float64, len(emb))}
			accum[personID] = a
		}
		for i, v := range emb {
			a.sum[i] += float64(v) * weight
		}
		a.totalWeight += weight
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	centroids := make(map[int64][]float32, len(accum))
	for personID, a := range accum {
		centroid := make([]float32, len(a.sum))
		for i, v := range a.sum {
			centroid[i] = float32(v / a.totalWeight)
		}
		l2normalize(centroid)
		centroids[personID] = centroid
	}
	return centroids, nil
}

// loadRecentFaces returns the K most recent face embeddings per person,
// used for chain matching to handle aging faces.
func loadRecentFaces(db *sql.DB, k int) (map[int64][][]float32, error) {
	// Use a window function to rank faces by date per person, then take top K.
	rows, err := db.Query(`SELECT person_id, embedding FROM (
		SELECT f.person_id, f.embedding,
			ROW_NUMBER() OVER (PARTITION BY f.person_id ORDER BY COALESCE(m.date_taken, m.date_indexed) DESC) AS rn
		FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NOT NULL AND f.embedding IS NOT NULL
	) WHERE rn <= ?`, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64][][]float32)
	for rows.Next() {
		var personID int64
		var blob []byte
		if err := rows.Scan(&personID, &blob); err != nil {
			continue
		}
		emb := bytesToFloat32Slice(blob)
		if len(emb) > 0 {
			result[personID] = append(result[personID], emb)
		}
	}
	return result, rows.Err()
}

// loadPersonBirthYears infers a birth year for each person by averaging
// (photo_year - estimated_age) across all their faces with age estimates.
func loadPersonBirthYears(db *sql.DB) (map[int64]float64, error) {
	rows, err := db.Query(`SELECT f.person_id,
		AVG(CAST(COALESCE(m.date_taken, m.date_indexed) AS REAL) / 31557600.0 + 1970 - f.estimated_age)
		FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NOT NULL AND f.estimated_age >= 0
		GROUP BY f.person_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[int64]float64)
	for rows.Next() {
		var personID int64
		var birthYear float64
		if err := rows.Scan(&personID, &birthYear); err != nil {
			continue
		}
		result[personID] = birthYear
	}
	return result, rows.Err()
}

// agePenalty computes a distance penalty when the face's estimated age doesn't match
// the person's expected age at the photo's date. Returns 0 when age data is unavailable
// or the mismatch is within tolerance.
func agePenalty(estimatedAge float64, photoDate int64, birthYear float64) float64 {
	if estimatedAge < 0 || birthYear == 0 {
		return 0
	}
	photoYear := float64(photoDate)/31557600.0 + 1970
	expectedAge := photoYear - birthYear
	ageError := math.Abs(estimatedAge - expectedAge)
	// 5-year tolerance for estimation noise, then 0.02 penalty per year beyond that.
	if ageError <= 5 {
		return 0
	}
	return (ageError - 5) * 0.02
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
