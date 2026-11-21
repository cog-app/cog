package gallery

import (
	"database/sql"
	"log/slog"
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

func groupsOf(t *testing.T, db *sql.DB) map[int64][]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT group_id, media_id FROM duplicate_members ORDER BY group_id, media_id`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var g, m int64
		require.NoError(t, rows.Scan(&g, &m))
		out[g] = append(out[g], m)
	}
	require.NoError(t, rows.Err())
	return out
}

func keepOf(t *testing.T, db *sql.DB, groupID int64) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.QueryRow(`SELECT media_id FROM duplicate_members WHERE group_id = ? AND is_keep = 1`, groupID).Scan(&id))
	return id
}

// What someone decides about a duplicate group survives the next scan,
// which rebuilds the groups from the fingerprints.
func TestDuplicateDecisionsSurviveRebuild(t *testing.T) {
	db := testDB(t)
	log := slog.New(slog.DiscardHandler)
	ids := make([]int64, 5)
	for i := range ids {
		ids[i] = insertTestMedia(t, db, int64(1000+i))
	}
	// 0, 1, 2 share a fingerprint; 3 and 4 share another.
	for i, fp := range []string{"ff00", "ff00", "ff00", "00ff", "00ff"} {
		_, err := db.Exec(`UPDATE media SET fingerprint = ? WHERE id = ?`, fp, ids[i])
		require.NoError(t, err)
	}
	require.NoError(t, rebuildDuplicateGroups(db, 0, log))
	groups := groupsOf(t, db)
	require.Equal(t, 2, len(groups))
	var big, small int64
	for g, members := range groups {
		if len(members) == 3 {
			big = g
		} else {
			small = g
		}
	}

	require.NoError(t, SetKeepMember(db, big, ids[2]))
	require.NoError(t, RemoveDuplicateMember(db, big, ids[0]))
	require.NoError(t, DismissDuplicateGroup(db, small))

	require.NoError(t, rebuildDuplicateGroups(db, 0, log))
	groups = groupsOf(t, db)
	require.Equal(t, map[int64][]int64{big: {ids[1], ids[2]}}, groups)
	require.Equal(t, ids[2], keepOf(t, db, big))
}

// A smaller copy is never kept over its original, however sharp its
// downscaled thumbnail looks.
func TestBetterKeepPrefersResolution(t *testing.T) {
	original := fpEntry{Width: 4000, Height: 3000, QualityScore: 10}
	copy := fpEntry{Width: 1600, Height: 1200, QualityScore: 30}
	require.True(t, betterKeep(original, copy))
	require.False(t, betterKeep(copy, original))
}
