package gallery_test

import (
	"bytes"
	"context"
	"database/sql"
	"image"
	"image/color"
	"image/jpeg"
	"math/rand/v2"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

// photoJPEG draws a noisy gradient, distinct per seed, so fingerprints and
// content keys differ between photos.
func photoJPEG(t *testing.T, seed uint64, w, h int) []byte {
	t.Helper()
	r := rand.New(rand.NewPCG(seed, seed))
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := uint8(r.IntN(200))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{base + uint8(x*50/w), uint8(y * 200 / h), uint8(r.IntN(30)), 255})
		}
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}))
	return buf.Bytes()
}

type scanEnv struct {
	*cogtest.Env
	g     *gallery.Plugin
	alice *cogtest.User
	db    *sql.DB
}

func newScanEnv(t *testing.T) *scanEnv {
	t.Helper()
	g := gallery.New()
	env := cogtest.New(t, g)
	alice := env.CreateUser(t, "alice", "pass")
	db, err := g.DB(alice.Username)
	require.NoError(t, err)
	return &scanEnv{Env: env, g: g, alice: alice, db: db}
}

func (e *scanEnv) upload(t *testing.T, path string, data []byte) {
	t.Helper()
	e.PostText(t, "/api/files/upload?path="+url.QueryEscape(path), string(data), e.alice.Auth).Status(201)
}

func (e *scanEnv) scan(t *testing.T) {
	t.Helper()
	require.NoError(t, e.g.ScanUser(context.Background(), e.alice.Username))
}

func (e *scanEnv) mediaID(t *testing.T, path string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, e.db.QueryRow(`SELECT id FROM media WHERE file_path = ?`, path).Scan(&id))
	return id
}

// Renaming a photo gives it a new etag, but not new content: the gallery
// keeps what it worked out about it rather than starting again.
func TestScanKeepsAnalysisAcrossRename(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/a.jpg", photoJPEG(t, 1, 640, 480))
	e.scan(t)

	id := e.mediaID(t, "/Photos/a.jpg")
	_, err := e.db.Exec(`UPDATE media SET recognition_state = 1 WHERE id = ?`, id)
	require.NoError(t, err)
	var fingerprint string
	require.NoError(t, e.db.QueryRow(`SELECT fingerprint FROM media WHERE id = ?`, id).Scan(&fingerprint))
	require.True(t, fingerprint != "" && fingerprint != "-", "fingerprint %q", fingerprint)

	e.PostJSON(t, "/api/files/rename", map[string]string{"old_path": "/Photos/a.jpg", "new_path": "/Photos/b.jpg"}, e.alice.Auth).Status(200)
	e.scan(t)

	require.Equal(t, id, e.mediaID(t, "/Photos/b.jpg"))
	var state int
	var after string
	require.NoError(t, e.db.QueryRow(`SELECT recognition_state, fingerprint FROM media WHERE id = ?`, id).Scan(&state, &after))
	require.Equal(t, 1, state)
	require.Equal(t, fingerprint, after)
}

// A changed photo is analysed again.
func TestScanResetsChangedFile(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/a.jpg", photoJPEG(t, 1, 640, 480))
	e.scan(t)
	id := e.mediaID(t, "/Photos/a.jpg")
	_, err := e.db.Exec(`UPDATE media SET recognition_state = 1 WHERE id = ?`, id)
	require.NoError(t, err)

	e.upload(t, "/Photos/a.jpg", photoJPEG(t, 2, 800, 600))
	e.scan(t)

	var state, width int
	require.NoError(t, e.db.QueryRow(`SELECT recognition_state, width FROM media WHERE id = ?`, id).Scan(&state, &width))
	require.Equal(t, 0, state)
	require.Equal(t, 800, width)
}

