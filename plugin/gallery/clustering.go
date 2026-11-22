package gallery

import (
	"cmp"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"time"
)

const (
	defaultClusterThreshold = 0.35 // cosine distance threshold for face clustering

	// seedFaceQuality is the faceQuality a face needs to found a person.
	seedFaceQuality = 0.4
	// poorFaceMatch scales the threshold for a face below seedFaceQuality:
	// its embedding is less reliable, so it must be closer to join anyone.
	poorFaceMatch = 0.8
)

// faceQuality rates how far a face's embedding can be trusted, 0 to 1: the
// detector's confidence, times how much of ArcFace's 112 px input the face
// fills, times how frontal it is. Turning away is judged by how far the
// nose sits from between the eyes; without landmarks the face is taken as
// frontal.
func faceQuality(confidence, widthPx, heightPx float64, landmarks [][2]float64, imgW, imgH float64) float64 {
	size := math.Min(math.Min(widthPx, heightPx)/112, 1)
	frontal := 1.0
	if len(landmarks) == 5 && imgW > 0 && imgH > 0 {
		lx, rx := landmarks[0][0]*imgW, landmarks[1][0]*imgW
		ly, ry := landmarks[0][1]*imgH, landmarks[1][1]*imgH
		eyes := math.Hypot(rx-lx, ry-ly)
		if eyes > 0 {
			yaw := math.Abs(landmarks[2][0]*imgW-(lx+rx)/2) / eyes
			frontal = math.Max(0, math.Min(1, 1-2*yaw))
		}
	}
	return math.Max(0, math.Min(1, confidence)) * size * frontal
}

// clusterFaces assigns unassigned faces to existing people or creates new clusters.
// If sysDB is non-nil, the threshold is read from system settings.
// followedClause is true of a face f, on photo m, whose person is the owner's
// of a people list shared with the user: clustering leaves it be.
const followedClause = `(f.owner_face_id IS NOT NULL AND m.shared_from IN (SELECT linked_from FROM people WHERE linked_from != ''))`

func clusterFaces(db *sql.DB, sysDB *sql.DB) error {
	threshold := defaultClusterThreshold
	agePenaltyWeight := 0.0
	if sysDB != nil {
		threshold = SettingClusterThreshold.MustGet(sysDB)
		agePenaltyWeight = SettingAgePenaltyWeight.MustGet(sysDB)
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

	rows, err := db.Query(`SELECT f.id, f.media_id, f.embedding, f.estimated_age,
		COALESCE(m.date_taken, m.date_indexed), f.quality, f.confidence, f.w * m.width, f.h * m.height
		FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NULL AND f.embedding IS NOT NULL AND NOT ` + followedClause)
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
		quality      float64
	}
	var faces []unassignedFace
	for rows.Next() {
		var f unassignedFace
		var blob []byte
		var conf, wpx, hpx float64
		if err := rows.Scan(&f.id, &f.mediaID, &blob, &f.estimatedAge, &f.photoDate, &f.quality, &conf, &wpx, &hpx); err != nil {
			return fmt.Errorf("scan unassigned face: %w", err)
		}
		if f.quality < 0 {
			f.quality = faceQuality(conf, wpx, hpx, nil, 0, 0)
		}
		f.embedding = bytesToFloat32Slice(blob)
		if len(f.embedding) > 0 {
			l2normalize(f.embedding)
			faces = append(faces, f)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// The best faces go first, so a person is founded on a clear face and the
	// poor ones are then judged against it.
	slices.SortStableFunc(faces, func(a, b unassignedFace) int { return cmp.Compare(b.quality, a.quality) })

	type candidate struct {
		personID  int64
		embedding []float32
		birthYear float64
	}
	people := make([]candidate, 0, len(centroids))
	for personID, centroid := range centroids {
		people = append(people, candidate{personID, centroid, birthYears[personID]})
	}
	var recent []candidate
	for personID, embs := range recentFaces {
		for _, emb := range embs {
			l2normalize(emb)
			recent = append(recent, candidate{personID, emb, birthYears[personID]})
		}
	}
	closest := func(face unassignedFace, among []candidate, personID int64, best float64) (int64, float64) {
		for _, c := range among {
			dist := unitDistance(face.embedding, c.embedding)
			dist += agePenalty(face.estimatedAge, face.photoDate, c.birthYear, agePenaltyWeight)
			if dist < best {
				best, personID = dist, c.personID
			}
		}
		return personID, best
	}

	err = inTx(db, func(tx *sql.Tx) error {
		for _, face := range faces {
			good := face.quality >= seedFaceQuality
			limit := threshold
			if !good {
				limit = threshold * poorFaceMatch
			}
			bestPersonID, bestDist := closest(face, people, 0, limit)
			// Chain matching: if centroid didn't match, try recent faces.
			if bestPersonID == 0 {
				bestPersonID, _ = closest(face, recent, 0, bestDist)
			}

			switch {
			case bestPersonID > 0:
				if _, err := tx.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, bestPersonID, face.id); err != nil {
					return fmt.Errorf("assign face %d: %w", face.id, err)
				}
			case good:
				personID, err := createPersonFromFace(tx, face.id)
				if err != nil {
					return err
				}
				people = append(people, candidate{personID, face.embedding, 0})
			default:
				// A blurred, tiny or turned-away face that matches no one is
				// left unassigned rather than made a person of its own: it
				// would found a cluster that anyone's poor faces then join.
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	// Faces without embeddings (e.g. Pigo-only, no ArcFace) can't be clustered.
	// Create a separate person for each so the user can merge manually.
	var noEmbFaceIDs []int64
	noEmbRows, err := db.Query(`SELECT f.id FROM faces f JOIN media m ON m.id = f.media_id
		WHERE f.person_id IS NULL AND f.embedding IS NULL AND NOT ` + followedClause)
	if err != nil {
		return err
	}
	for noEmbRows.Next() {
		var faceID int64
		if err := noEmbRows.Scan(&faceID); err != nil {
			noEmbRows.Close()
			return fmt.Errorf("scan embeddingless face: %w", err)
		}
		noEmbFaceIDs = append(noEmbFaceIDs, faceID)
	}
	noEmbRows.Close()
	if err := noEmbRows.Err(); err != nil {
		return err
	}

	for _, faceID := range noEmbFaceIDs {
		if _, err := CreatePersonFromFace(db, faceID); err != nil {
			return err
		}
	}

	return nil
}

// clusterChanged clusters a user's faces unless nothing clustering reads
// has changed since it last did. With thousands of people, comparing the
// faces that matched no one against all of them again takes minutes, and
// would find what it found before.
func (p *Plugin) clusterChanged(db, sysDB *sql.DB, username string) error {
	before, err := clusterInputs(db, sysDB)
	if err != nil {
		return err
	}
	if last, ok := p.clustered.Load(username); ok && last == before {
		return nil
	}
	if err := clusterFaces(db, sysDB); err != nil {
		return err
	}
	after, err := clusterInputs(db, sysDB)
	if err != nil {
		return err
	}
	p.clustered.Store(username, after)
	return nil
}

// clusterInputs summarises the faces, whom they belong to, the people and
// the settings, so that a change to any of them changes it.
func clusterInputs(db, sysDB *sql.DB) (string, error) {
	var faces, people string
	if err := db.QueryRow(`SELECT
		(SELECT count(*) || ':' || COALESCE(max(id), 0) || ':' || total(person_id) || ':' || count(person_id) || ':' || count(embedding) FROM faces),
		(SELECT count(*) || ':' || COALESCE(max(id), 0) || ':' || total(length(linked_from)) FROM people)`).Scan(&faces, &people); err != nil {
		return "", fmt.Errorf("cluster inputs: %w", err)
	}
	return fmt.Sprintf("%s/%s/%v/%v", faces, people,
		SettingClusterThreshold.MustGet(sysDB), SettingAgePenaltyWeight.MustGet(sysDB)), nil
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
			return nil, err
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
			return nil, err
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
			return nil, err
		}
		result[personID] = birthYear
	}
	return result, rows.Err()
}

// agePenalty computes a distance penalty when the face's estimated age doesn't match
// the person's expected age at the photo's date. Returns 0 when age data is unavailable,
// the mismatch is within tolerance, or the penalty weight is 0 (disabled).
func agePenalty(estimatedAge float64, photoDate int64, birthYear float64, weight float64) float64 {
	if weight <= 0 || estimatedAge < 0 || birthYear == 0 {
		return 0
	}
	photoYear := float64(photoDate)/31557600.0 + 1970
	expectedAge := photoYear - birthYear
	ageError := math.Abs(estimatedAge - expectedAge)
	// 5-year tolerance for estimation noise, then weight penalty per year beyond that.
	if ageError <= 5 {
		return 0
	}
	return (ageError - 5) * weight
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

// unitDistance is cosineDistance for vectors already of unit length: the
// norms are left out of the loop clustering spends its time in.
func unitDistance(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1.0
	}
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return 1 - dot
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
