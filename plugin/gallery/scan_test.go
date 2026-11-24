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
	"strconv"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/vfs"
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

func (e *scanEnv) folderAlbums(t *testing.T) map[string][]string {
	t.Helper()
	rows, err := e.db.Query(`SELECT a.auto_key, m.file_path FROM albums a
		JOIN album_media am ON am.album_id = a.id JOIN media m ON m.id = am.media_id
		WHERE a.album_type = ? ORDER BY a.auto_key, m.file_path`, gallery.AlbumFolder)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var k, p string
		require.NoError(t, rows.Scan(&k, &p))
		out[k] = append(out[k], p)
	}
	require.NoError(t, rows.Err())
	return out
}

// A photo moved to another folder leaves its old folder's album, and an
// album left empty goes.
func TestScanFolderAlbumsFollowMoves(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/Trip/a.jpg", photoJPEG(t, 1, 64, 48))
	e.upload(t, "/Photos/Home/b.jpg", photoJPEG(t, 2, 64, 48))
	e.scan(t)
	require.Equal(t, map[string][]string{
		"/Photos/Home": {"/Photos/Home/b.jpg"},
		"/Photos/Trip": {"/Photos/Trip/a.jpg"},
	}, e.folderAlbums(t))

	e.PostJSON(t, "/api/files/rename", map[string]string{"old_path": "/Photos/Trip/a.jpg", "new_path": "/Photos/Home/a.jpg"}, e.alice.Auth).Status(200)
	e.scan(t)
	require.Equal(t, map[string][]string{
		"/Photos/Home": {"/Photos/Home/a.jpg", "/Photos/Home/b.jpg"},
	}, e.folderAlbums(t))
	var n int
	require.NoError(t, e.db.QueryRow(`SELECT COUNT(*) FROM albums WHERE album_type = ? AND auto_key = '/Photos/Trip'`, gallery.AlbumFolder).Scan(&n))
	require.Equal(t, 0, n)
}

// A photo no stage can read is marked as failed, with why, rather than
// coming back to every scan.
func TestScanSettlesUndecodablePhoto(t *testing.T) {
	e := newScanEnv(t)
	good := photoJPEG(t, 1, 64, 48)
	broken := append(append([]byte{}, good[:200]...), make([]byte, 300)...)
	e.upload(t, "/Photos/good.jpg", good)
	e.upload(t, "/Photos/broken.jpg", broken)
	e.scan(t)

	var quality float64
	var fingerprint, scanErr string
	require.NoError(t, e.db.QueryRow(`SELECT quality_score, fingerprint, scan_error FROM media WHERE file_path = '/Photos/broken.jpg'`).
		Scan(&quality, &fingerprint, &scanErr))
	require.Equal(t, -2.0, quality)
	require.Equal(t, "-", fingerprint)
	require.Contains(t, scanErr, "decode")

	pending, err := gallery.PendingWork(e.db)
	require.NoError(t, err)
	require.Equal(t, 0, pending.Quality)
	require.Equal(t, 0, pending.Fingerprint)
	require.Equal(t, 1, pending.Failed)

	var id int64
	require.NoError(t, e.db.QueryRow(`SELECT id FROM media WHERE file_path = '/Photos/broken.jpg'`).Scan(&id))
	e.Get(t, "/admin/plugins/gallery", e.alice.Auth).Status(200).
		Contains(`<a href="/apps/gallery/photo/` + strconv.FormatInt(id, 10) + `">/Photos/broken.jpg</a>`)
}

// withOrientation inserts an EXIF APP1 segment carrying an orientation tag
// after a JPEG's SOI marker.
func withOrientation(jpg []byte, orientation uint16) []byte {
	tiff := []byte("II*\x00\x08\x00\x00\x00\x01\x00\x12\x01\x03\x00\x01\x00\x00\x00")
	tiff = append(tiff, byte(orientation), byte(orientation>>8), 0, 0, 0, 0, 0, 0)
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	out := append([]byte{}, jpg[:2]...)
	out = append(out, 0xff, 0xe1, byte((len(app1)+2)>>8), byte(len(app1)+2))
	out = append(out, app1...)
	return append(out, jpg[2:]...)
}

// A photo stored on its side is measured upright, as its thumbnail and
// face boxes are.
func TestScanStoresUprightDimensions(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/portrait.jpg", withOrientation(photoJPEG(t, 1, 400, 300), 6))
	e.scan(t)
	var w, h, o int
	require.NoError(t, e.db.QueryRow(`SELECT width, height, orientation FROM media`).Scan(&w, &h, &o))
	require.Equal(t, 6, o)
	require.Equal(t, 300, w)
	require.Equal(t, 400, h)
}

// The admin page says, per user, what the scan still has to do.
func TestAdminPageShowsPendingWork(t *testing.T) {
	e := newScanEnv(t) // alice is the first user, so the admin
	e.upload(t, "/Photos/a.jpg", photoJPEG(t, 1, 64, 48))
	e.Get(t, "/admin/plugins/gallery", e.alice.Auth).Status(200).Contains("Waiting for").Contains("<td>alice</td>")
}

// A photo with no date of its own is placed by its file's time, not by the
// day the gallery found it.
func TestUndatedPhotoTakesFileTime(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/old.jpg", photoJPEG(t, 1, 64, 64))
	fsys, err := e.Providers.Shared.Files.FS("alice")
	require.NoError(t, err)
	mtime := time.Date(2011, time.June, 5, 10, 0, 0, 0, time.UTC)
	require.NoError(t, fsys.(vfs.AttrSetter).Chtimes("/Photos/old.jpg", mtime, mtime))
	e.scan(t)

	var taken sql.NullInt64
	var dated int64
	require.NoError(t, e.db.QueryRow(`SELECT date_taken, date_indexed FROM media WHERE file_path = '/Photos/old.jpg'`).Scan(&taken, &dated))
	require.False(t, taken.Valid)
	require.Equal(t, mtime.Unix(), dated)
}

// A new library's thumbnails take hours; a round making them stops at the
// deadline so the users after it get their turn.
func TestThumbnailsStopAtDeadline(t *testing.T) {
	e := newScanEnv(t)
	sysDB, err := e.g.SystemDB()
	require.NoError(t, err)
	require.NoError(t, gallery.SettingPreviewGenerationEnabled.Set(sysDB, false))
	e.upload(t, "/Photos/a.jpg", photoJPEG(t, 1, 64, 64))
	e.scan(t)

	remaining, err := e.g.PreGenerateThumbnails(context.Background(), e.db, e.alice.Username, time.Now().Add(-time.Second))
	require.NoError(t, err)
	require.True(t, remaining)

	remaining, err = e.g.PreGenerateThumbnails(context.Background(), e.db, e.alice.Username, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.False(t, remaining)
}

// A video's cached thumbnail is named by its etag, which a rename changes;
// the scan that sees the move carries the thumbnail to the new name.
func TestScanCarriesVideoThumbnailAcrossRename(t *testing.T) {
	e := newScanEnv(t)
	e.upload(t, "/Photos/clip.mp4", []byte("not really a video"))
	e.scan(t)
	etag := func() string {
		var s string
		require.NoError(t, e.db.QueryRow(`SELECT file_etag FROM media WHERE file_path LIKE '%.mp4'`).Scan(&s))
		return s
	}
	cache, err := e.g.UserCacheForTest(e.alice.Username)
	require.NoError(t, err)
	_, err = cache.Put("video_thumbs/"+etag()+"_256.jpg", bytes.NewReader([]byte("thumb")))
	require.NoError(t, err)

	req := cogtest.NewRequest(t, "MOVE", e.Providers.Config.BaseURL+"/remote.php/dav/files/alice/Photos/clip.mp4", nil)
	e.Do(t, req, e.alice.Auth, cogtest.Header("Destination", "/remote.php/dav/files/alice/Photos/renamed.mp4")).Status(201)
	e.scan(t)
	_, err = cache.Stat("video_thumbs/" + etag() + "_256.jpg")
	require.NoError(t, err)
}
