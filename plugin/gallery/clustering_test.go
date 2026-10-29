package gallery

import (
	"database/sql"
	"math"
	"math/rand/v2"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Unit tests ---

func TestCosineDistance(t *testing.T) {
	a := make([]float32, 512)
	b := make([]float32, 512)

	t.Run("identical", func(t *testing.T) {
		a[0] = 1
		b[0] = 1
		assert.Equal(t, 0.0, cosineDistance(a, b))
	})

	t.Run("orthogonal", func(t *testing.T) {
		a = make([]float32, 512)
		b = make([]float32, 512)
		a[0] = 1
		b[1] = 1
		d := cosineDistance(a, b)
		assert.True(t, math.Abs(d-1.0) < 1e-6, "expected ~1.0, got %f", d)
	})

	t.Run("opposite", func(t *testing.T) {
		a = make([]float32, 512)
		b = make([]float32, 512)
		a[0] = 1
		b[0] = -1
		d := cosineDistance(a, b)
		assert.True(t, math.Abs(d-2.0) < 1e-6, "expected ~2.0, got %f", d)
	})

	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, 1.0, cosineDistance(nil, nil))
		assert.Equal(t, 1.0, cosineDistance([]float32{}, []float32{}))
	})

	t.Run("different lengths", func(t *testing.T) {
		assert.Equal(t, 1.0, cosineDistance([]float32{1}, []float32{1, 2}))
	})
}

func TestAgePenalty(t *testing.T) {
	// Unix timestamp for 2024-06-15.
	date2024 := int64(1718409600)

	w := 0.02 // default weight

	t.Run("no age data", func(t *testing.T) {
		assert.Equal(t, 0.0, agePenalty(-1, date2024, 2019, w))
		assert.Equal(t, 0.0, agePenalty(5, date2024, 0, w))
	})

	t.Run("disabled", func(t *testing.T) {
		assert.Equal(t, 0.0, agePenalty(15, date2024, 2019, 0))
	})

	t.Run("within tolerance", func(t *testing.T) {
		assert.Equal(t, 0.0, agePenalty(5, date2024, 2019, w))
		assert.Equal(t, 0.0, agePenalty(8, date2024, 2019, w))
	})

	t.Run("beyond tolerance", func(t *testing.T) {
		// Error 10, penalty = (10-5)*0.02 = 0.10.
		p := agePenalty(15, date2024, 2019, w)
		assert.True(t, math.Abs(p-0.10) < 0.01, "expected ~0.10, got %f", p)
	})
}

func TestFloat32Roundtrip(t *testing.T) {
	original := []float32{1.0, -0.5, 3.14159, 0, math.MaxFloat32}
	encoded := float32SliceToBytes(original)
	decoded := bytesToFloat32Slice(encoded)
	require.Equal(t, len(original), len(decoded))
	for i := range original {
		assert.Equal(t, original[i], decoded[i])
	}
}

// --- Integration tests ---

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "gallery.db")+"?_busy_timeout=5000&_foreign_keys=on")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	require.NoError(t, migrate.Migrate(db, sqlFS))
	return db
}

func insertTestMedia(t *testing.T, db *sql.DB, dateTaken int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_taken, date_indexed)
		VALUES (?, 'test.jpg', 'etag', 'image/jpeg', 1000, 1000, ?, ?)`,
		rand.Int64(), dateTaken, dateTaken)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return id
}

func insertTestFace(t *testing.T, db *sql.DB, mediaID int64, embedding []float32, age float64) int64 {
	t.Helper()
	var blob []byte
	if embedding != nil {
		blob = float32SliceToBytes(embedding)
	}
	res, err := db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, estimated_age)
		VALUES (?, 0.3, 0.3, 0.4, 0.4, ?, 0.9, ?)`, mediaID, blob, age)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return id
}

func assignFace(t *testing.T, db *sql.DB, faceID, personID int64) {
	t.Helper()
	_, err := db.Exec(`UPDATE faces SET person_id = ? WHERE id = ?`, personID, faceID)
	require.NoError(t, err)
}

func createPerson(t *testing.T, db *sql.DB, name string, repFaceID int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO people (name, representative_face_id) VALUES (?, ?)`, name, repFaceID)
	require.NoError(t, err)
	id, _ := res.LastInsertId()
	return id
}

func personForFace(t *testing.T, db *sql.DB, faceID int64) int64 {
	t.Helper()
	var personID sql.NullInt64
	require.NoError(t, db.QueryRow(`SELECT person_id FROM faces WHERE id = ?`, faceID).Scan(&personID))
	if personID.Valid {
		return personID.Int64
	}
	return 0
}

func countPeople(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM people`).Scan(&n))
	return n
}

// makeEmbedding creates a unit-length 512-dim embedding pointing primarily in the
// given direction (0-511), with optional noise for realistic variation.
func makeEmbedding(direction int, noise float64) []float32 {
	emb := make([]float32, 512)
	emb[direction%512] = 1.0
	if noise > 0 {
		for i := range emb {
			emb[i] += float32(noise * (rand.Float64()*2 - 1))
		}
	}
	l2normalize(emb)
	return emb
}

func TestClusterBasic(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600) // 2024

	// 3 faces of person A, 2 faces of person B.
	for range 3 {
		mid := insertTestMedia(t, db, date)
		insertTestFace(t, db, mid, makeEmbedding(0, 0.01), -1)
	}
	for range 2 {
		mid := insertTestMedia(t, db, date)
		insertTestFace(t, db, mid, makeEmbedding(1, 0.01), -1)
	}

	require.NoError(t, clusterFaces(db, nil))
	assert.Equal(t, 2, countPeople(t, db))
}

func TestClusterSamePersonMultipleFaces(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600)

	for range 5 {
		mid := insertTestMedia(t, db, date)
		insertTestFace(t, db, mid, makeEmbedding(0, 0.02), -1)
	}

	require.NoError(t, clusterFaces(db, nil))
	assert.Equal(t, 1, countPeople(t, db))
}

func TestClusterSiblingAgeDisambiguation(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600) // 2024

	// Two siblings with very similar embeddings but different ages.
	// Embeddings are close enough that cosine distance alone merges them,
	// but the age gap (5 vs 25) pushes the effective distance over threshold.
	embA := make([]float32, 512)
	embA[0] = 1.0
	embA[1] = 0.15
	l2normalize(embA)

	embB := make([]float32, 512)
	embB[0] = 1.0
	embB[1] = -0.15
	l2normalize(embB)

	// Verify they're close in cosine distance.
	dist := cosineDistance(embA, embB)
	t.Logf("sibling cosine distance: %f (threshold: %f)", dist, defaultClusterThreshold)
	assert.True(t, dist < defaultClusterThreshold, "test setup: siblings should be within threshold without age penalty")

	// Create first "person" (child, age 5) with an existing assigned face.
	mid1 := insertTestMedia(t, db, date)
	f1 := insertTestFace(t, db, mid1, embA, 5)
	p1 := createPerson(t, db, "Child", f1)
	assignFace(t, db, f1, p1)

	// New unassigned face of parent (age 25) — similar embedding but very different age.
	// Age error = |25 - 5| = 20, penalty = (20-5)*0.02 = 0.30.
	// Total effective distance = ~0.044 + 0.30 = ~0.34, just under threshold.
	// Use age 30 to make it clearly over: penalty = (25-5)*0.02 = 0.40, total ~0.44.
	mid2 := insertTestMedia(t, db, date)
	insertTestFace(t, db, mid2, embB, 30)

	// Enable age penalty for this test.
	SettingAgePenaltyWeight.Set(db, 0.02)
	require.NoError(t, clusterFaces(db, db))

	// Should create a new person, not merge into child 1.
	assert.Equal(t, 2, countPeople(t, db))
}

func TestClusterBridgeEffect(t *testing.T) {
	db := testDB(t)
	date2020 := int64(1577836800)
	date2023 := int64(1672531200)
	date2024 := int64(1718409600)

	// Person with early face (2020).
	earlyEmb := makeEmbedding(0, 0)
	mid1 := insertTestMedia(t, db, date2020)
	f1 := insertTestFace(t, db, mid1, earlyEmb, -1)
	pid := createPerson(t, db, "Aging Person", f1)
	assignFace(t, db, f1, pid)

	// Intermediate face (2023) — slightly drifted.
	midEmb := make([]float32, 512)
	midEmb[0] = 0.9
	midEmb[2] = 0.1
	l2normalize(midEmb)
	mid2 := insertTestMedia(t, db, date2023)
	f2 := insertTestFace(t, db, mid2, midEmb, -1)
	assignFace(t, db, f2, pid)

	// New face (2024) — further drifted but close to the 2023 face.
	newEmb := make([]float32, 512)
	newEmb[0] = 0.8
	newEmb[2] = 0.2
	l2normalize(newEmb)
	mid3 := insertTestMedia(t, db, date2024)
	newFace := insertTestFace(t, db, mid3, newEmb, -1)

	require.NoError(t, clusterFaces(db, nil))

	// Should be assigned to the existing person via centroid or chain match.
	assert.Equal(t, pid, personForFace(t, db, newFace))
}

func TestClusterNoEmbedding(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600)

	// 3 faces with no embeddings (Pigo-only).
	for range 3 {
		mid := insertTestMedia(t, db, date)
		insertTestFace(t, db, mid, nil, -1)
	}

	require.NoError(t, clusterFaces(db, nil))

	// Each gets its own person.
	assert.Equal(t, 3, countPeople(t, db))
}

func TestClusterIncrementalAssignment(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600)

	// Create two existing people with assigned faces.
	midA := insertTestMedia(t, db, date)
	fA := insertTestFace(t, db, midA, makeEmbedding(0, 0.01), -1)
	pA := createPerson(t, db, "Person A", fA)
	assignFace(t, db, fA, pA)

	midB := insertTestMedia(t, db, date)
	fB := insertTestFace(t, db, midB, makeEmbedding(1, 0.01), -1)
	pB := createPerson(t, db, "Person B", fB)
	assignFace(t, db, fB, pB)

	// New unassigned faces matching each person.
	midNewA := insertTestMedia(t, db, date)
	newA := insertTestFace(t, db, midNewA, makeEmbedding(0, 0.02), -1)
	midNewB := insertTestMedia(t, db, date)
	newB := insertTestFace(t, db, midNewB, makeEmbedding(1, 0.02), -1)

	require.NoError(t, clusterFaces(db, nil))

	assert.Equal(t, pA, personForFace(t, db, newA))
	assert.Equal(t, pB, personForFace(t, db, newB))
	assert.Equal(t, 2, countPeople(t, db))
}

func TestClusterRecencyWeighting(t *testing.T) {
	db := testDB(t)
	date2015 := int64(1420070400)
	date2024 := int64(1718409600)

	// Person with old faces (2015) pointing direction 0.
	for range 3 {
		mid := insertTestMedia(t, db, date2015)
		f := insertTestFace(t, db, mid, makeEmbedding(0, 0.01), -1)
		if countPeople(t, db) == 0 {
			pid := createPerson(t, db, "Evolving", f)
			assignFace(t, db, f, pid)
		} else {
			var pid int64
			db.QueryRow(`SELECT id FROM people LIMIT 1`).Scan(&pid)
			assignFace(t, db, f, pid)
		}
	}

	// Recent face (2024) pointing direction 2 (drifted).
	recentEmb := make([]float32, 512)
	recentEmb[0] = 0.3
	recentEmb[2] = 0.7
	l2normalize(recentEmb)
	mid := insertTestMedia(t, db, date2024)
	f := insertTestFace(t, db, mid, recentEmb, -1)
	var pid int64
	db.QueryRow(`SELECT id FROM people LIMIT 1`).Scan(&pid)
	assignFace(t, db, f, pid)

	// New face matching the recent appearance (direction 2-ish).
	newEmb := make([]float32, 512)
	newEmb[0] = 0.25
	newEmb[2] = 0.75
	l2normalize(newEmb)
	midNew := insertTestMedia(t, db, date2024)
	newFace := insertTestFace(t, db, midNew, newEmb, -1)

	require.NoError(t, clusterFaces(db, nil))

	// Should match the existing person (recency-weighted centroid favors the 2024 face).
	assert.Equal(t, pid, personForFace(t, db, newFace))
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow("SELECT COUNT(*) FROM "+table).Scan(&n))
	return n
}

func TestPurgeRecognitions(t *testing.T) {
	db := testDB(t)
	date := int64(1718409600)

	// Insert media, tags, faces, and people.
	mid := insertTestMedia(t, db, date)
	insertTestFace(t, db, mid, makeEmbedding(0, 0), -1)

	// Run clustering to create a person.
	require.NoError(t, clusterFaces(db, nil))
	assert.Equal(t, 1, countPeople(t, db))
	assert.Equal(t, 1, countRows(t, db, "faces"))

	// Add a tag.
	db.Exec(`INSERT INTO tags (name, tag_type) VALUES ('dog', 1)`)
	db.Exec(`INSERT INTO media_tags (media_id, tag_id, confidence) VALUES (?, 1, 0.9)`, mid)
	assert.Equal(t, 1, countRows(t, db, "tags"))
	assert.Equal(t, 1, countRows(t, db, "media_tags"))

	// Set recognition state to done.
	db.Exec(`UPDATE media SET recognition_state = 1 WHERE id = ?`, mid)

	// Purge.
	require.NoError(t, PurgeRecognitions(db))

	// Everything should be gone.
	assert.Equal(t, 0, countRows(t, db, "faces"))
	assert.Equal(t, 0, countRows(t, db, "people"))
	assert.Equal(t, 0, countRows(t, db, "tags"))
	assert.Equal(t, 0, countRows(t, db, "media_tags"))

	// Media should still exist but with recognition_state reset to 0.
	assert.Equal(t, 1, countRows(t, db, "media"))
	var state int
	require.NoError(t, db.QueryRow(`SELECT recognition_state FROM media WHERE id = ?`, mid).Scan(&state))
	assert.Equal(t, 0, state)
}

func TestSettingsSectionIndependence(t *testing.T) {
	db := testDB(t)

	// Simulate saving the recognition section — sets recognition_enabled.
	SetSetting(db, "recognition_enabled", "true")
	SetSetting(db, "recognition_backend", "onnx")

	// Simulate saving the duplicates section — sets duplicate_detection_enabled.
	SetSetting(db, "duplicate_detection_enabled", "true")
	SetSetting(db, "duplicate_algorithm", "difference")

	// Verify both are independently set.
	assert.Equal(t, "true", GetSetting(db, "recognition_enabled", ""))
	assert.Equal(t, "true", GetSetting(db, "duplicate_detection_enabled", ""))

	// Simulate saving recognition again without touching duplicate settings.
	// This is what the _section-based handler does — only update recognition keys.
	SetSetting(db, "recognition_enabled", "true")
	SetSetting(db, "recognition_backend", "pigo")
	// Crucially, we do NOT set duplicate_detection_enabled here.

	// Duplicate setting should still be "true".
	assert.Equal(t, "true", GetSetting(db, "duplicate_detection_enabled", ""))

	// Now simulate the OLD bug: saving recognition section also sets
	// duplicate_detection_enabled to "" (empty form value for unchecked checkbox).
	// This is what happened before the _section fix.
	SetSetting(db, "duplicate_detection_enabled", "")
	assert.Equal(t, "", GetSetting(db, "duplicate_detection_enabled", ""))
	// This demonstrates the bug — the empty string overwrites the setting.
}
