package gallery_test

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

type shareEnv struct {
	*scanEnv
	bob   *cogtest.User
	bobDB *sql.DB
	fp    *files.Plugin
}

// newShareEnv has alice share /Share with bob, who mounts it at
// /Photos/Alice. Her gallery scans /Share/Photos; the rest of the folder
// only bob's does.
func newShareEnv(t *testing.T) *shareEnv {
	t.Helper()
	e := newScanEnv(t)
	bob := e.CreateUser(t, "bob", "pass")
	fp := e.Providers.Shared.Files
	_, err := fp.Upload("alice", "/Share/Photos/a.jpg", bytes.NewReader(photoJPEG(t, 1, 640, 480)))
	require.NoError(t, err)
	_, err = fp.Upload("alice", "/Share/other.jpg", bytes.NewReader(photoJPEG(t, 2, 320, 240)))
	require.NoError(t, err)
	require.NoError(t, gallery.SettingScanFolders.Set(e.db, "Share/Photos/"))
	dir, err := fp.GetByPath("alice", "/Share")
	require.NoError(t, err)
	_, err = e.Providers.Shares.Create(e.alice.ID, mount.SharedDirType, files.IDString(dir), "USER", bob.ID, plugin.PermRead)
	require.NoError(t, err)
	entry, err := e.Providers.Shared.Lookup(bob.User, "alice", "Share")
	require.NoError(t, err)
	_, err = fp.CreateDir("bob", "/Photos")
	require.NoError(t, err)
	_, err = e.Providers.Shared.MountFolder(bob.User, entry, "/Photos/Alice")
	require.NoError(t, err)
	bobDB, err := e.g.DB("bob")
	require.NoError(t, err)
	return &shareEnv{scanEnv: e, bob: bob, bobDB: bobDB, fp: fp}
}

// scanBob indexes bob's photos, as asking for a scan does, and scans them.
func (e *shareEnv) scanBob(t *testing.T) {
	t.Helper()
	_, err := e.fp.ReconcileTree(context.Background(), "bob", "/Photos")
	require.NoError(t, err)
	require.NoError(t, e.g.ScanUser(context.Background(), "bob"))
}

func TestScanCopiesSharedPhotosFromOwner(t *testing.T) {
	e := newShareEnv(t)
	bobDB, fp := e.bobDB, e.fp
	e.scan(t)
	aliceID := e.mediaID(t, "/Share/Photos/a.jpg")
	_, err := e.db.Exec(`UPDATE media SET recognition_state = 1, recognition_at = 1000 WHERE id = ?`, aliceID)
	require.NoError(t, err)
	_, err = e.db.Exec(`INSERT INTO faces (media_id, x, y, w, h, embedding, confidence, quality) VALUES (?, 0.1, 0.2, 0.3, 0.4, x'0000803f', 0.9, 0.7)`, aliceID)
	require.NoError(t, err)
	tagID, err := gallery.UpsertTag(e.db, "beach", 2)
	require.NoError(t, err)
	require.NoError(t, gallery.AddMediaTag(e.db, aliceID, tagID, 0.8))

	e.scanBob(t)

	var from, fingerprint, theirs, makeType string
	var width int
	var indexed, theirIndexed int64
	require.NoError(t, bobDB.QueryRow(`SELECT shared_from, width, fingerprint, date_indexed, typeof(camera_make) FROM media WHERE file_path = '/Photos/Alice/Photos/a.jpg'`).
		Scan(&from, &width, &fingerprint, &indexed, &makeType))
	require.NoError(t, e.db.QueryRow(`SELECT fingerprint, date_indexed FROM media WHERE id = ?`, aliceID).Scan(&theirs, &theirIndexed))
	require.Equal(t, "alice", from)
	require.Equal(t, 640, width)
	require.Equal(t, theirs, fingerprint)
	require.Equal(t, theirIndexed, indexed) // copied, not read again
	require.Equal(t, "text", makeType)

	var faces, tags, state int
	require.NoError(t, bobDB.QueryRow(`SELECT
		(SELECT COUNT(*) FROM faces f JOIN media m ON m.id = f.media_id WHERE m.shared_from = 'alice' AND f.quality = 0.7),
		(SELECT COUNT(*) FROM media_tags mt JOIN tags t ON t.id = mt.tag_id WHERE t.name = 'beach'),
		(SELECT recognition_state FROM media WHERE shared_from = 'alice')`).Scan(&faces, &tags, &state))
	require.Equal(t, 1, faces)
	require.Equal(t, 1, tags)
	require.Equal(t, 1, state)

	require.NoError(t, bobDB.QueryRow(`SELECT shared_from, width FROM media WHERE file_path = '/Photos/Alice/other.jpg'`).Scan(&from, &width))
	require.Equal(t, "", from)
	require.Equal(t, 320, width)

	pending, err := gallery.PendingWork(bobDB)
	require.NoError(t, err)
	require.Equal(t, 0, pending.Metadata)

	// bob's thumbnail is the one alice's scan made.
	made, err := fp.EnsurePreview("bob", "/Photos/Alice/Photos/a.jpg", 256)
	require.NoError(t, err)
	require.False(t, made)

	// Recognition run again on alice's side reaches bob's next scan.
	_, err = e.db.Exec(`UPDATE media SET recognition_at = 2000 WHERE id = ?`, aliceID)
	require.NoError(t, err)
	_, err = e.db.Exec(`INSERT INTO faces (media_id, x, y, w, h, confidence) VALUES (?, 0.5, 0.5, 0.1, 0.1, 0.9)`, aliceID)
	require.NoError(t, err)
	e.scanBob(t)
	require.NoError(t, bobDB.QueryRow(`SELECT COUNT(*) FROM faces f JOIN media m ON m.id = f.media_id WHERE m.shared_from = 'alice'`).Scan(&faces))
	require.Equal(t, 2, faces)
}
