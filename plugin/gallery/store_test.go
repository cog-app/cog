package gallery

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// An upsert that resolves to an update leaves SQLite's last_insert_rowid
// untouched, so it reports whatever the connection inserted last. Pinning the
// pool to one connection makes that stale id certain rather than likely.
func TestUpsertReturnsExistingID(t *testing.T) {
	db := testDB(t)
	db.SetMaxIdleConns(1)

	tagID, err := UpsertTag(db, "dog", 1)
	require.NoError(t, err)
	albumID, err := UpsertAutoAlbum(db, 1, "key", "Album")
	require.NoError(t, err)

	// Advance last_insert_rowid well past both ids.
	for range 5 {
		insertTestMedia(t, db, 0)
	}

	again, err := UpsertTag(db, "dog", 1)
	require.NoError(t, err)
	require.Equal(t, tagID, again)

	againAlbum, err := UpsertAutoAlbum(db, 1, "key", "Album")
	require.NoError(t, err)
	require.Equal(t, albumID, againAlbum)
}
