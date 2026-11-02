package gallery

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// An upsert that resolves to an update leaves SQLite's last_insert_rowid
// untouched, so it reports whatever the connection inserted last. With one
// idle connection, these sequential calls all reuse the same connection,
// which makes that stale id certain rather than likely. (It caps idle
// connections, not open ones; nothing here runs concurrently.)
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

// Deleting every non-keep duplicate reads them all in one query: keep items
// and groups too small to show are left out, and a media item in two groups
// comes back once. Pruning then drops exactly the groups left with fewer
// than two members.
func TestNonKeepDuplicatesAndPrune(t *testing.T) {
	db := testDB(t)
	keepA, dupA, keepB, dupB, lone := insertTestMedia(t, db, 0), insertTestMedia(t, db, 0),
		insertTestMedia(t, db, 0), insertTestMedia(t, db, 0), insertTestMedia(t, db, 0)

	groupA, err := InsertDuplicateGroup(db)
	require.NoError(t, err)
	require.NoError(t, AddDuplicateMember(db, groupA, keepA, true, 0))
	require.NoError(t, AddDuplicateMember(db, groupA, dupA, false, 1))
	groupB, err := InsertDuplicateGroup(db)
	require.NoError(t, err)
	require.NoError(t, AddDuplicateMember(db, groupB, keepB, true, 0))
	require.NoError(t, AddDuplicateMember(db, groupB, dupB, false, 1))
	require.NoError(t, AddDuplicateMember(db, groupB, dupA, false, 2))
	small, err := InsertDuplicateGroup(db)
	require.NoError(t, err)
	require.NoError(t, AddDuplicateMember(db, small, lone, false, 0))

	members, err := NonKeepDuplicates(db)
	require.NoError(t, err)
	got := map[int64]bool{}
	for _, m := range members {
		require.False(t, got[m.Media.ID], "media returned twice")
		got[m.Media.ID] = true
	}
	require.Equal(t, map[int64]bool{dupA: true, dupB: true}, got)

	for id := range got {
		_, err := db.Exec(`DELETE FROM media WHERE id = ?`, id)
		require.NoError(t, err)
	}
	require.NoError(t, PruneDuplicateGroups(db))
	var left int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM duplicate_groups`).Scan(&left))
	require.Equal(t, 0, left)
}
