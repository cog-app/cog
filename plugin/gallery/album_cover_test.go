package gallery

import (
	"errors"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestAlbumCover covers choosing a cover and every way it falls back to the
// album's first photo: never chosen, taken out of the album, deleted, and a
// stale choice left by a version before the trigger.
func TestAlbumCover(t *testing.T) {
	db := testDB(t)
	cover := func(id int64) int64 {
		t.Helper()
		a, err := GetAlbum(db, id)
		require.NoError(t, err)
		if a.CoverMediaID == nil {
			return 0
		}
		return *a.CoverMediaID
	}

	album, err := CreateAlbumRow(db, "Trip", "")
	require.NoError(t, err)
	first := insertTestMedia(t, db, 1000)
	second := insertTestMedia(t, db, 2000)
	third := insertTestMedia(t, db, 3000)
	outside := insertTestMedia(t, db, 4000)
	for _, m := range []int64{first, second, third} {
		require.NoError(t, AddMediaToAlbum(db, album, m))
	}

	// No choice: the first photo.
	require.Equal(t, first, cover(album))

	// A chosen cover sticks.
	require.NoError(t, SetAlbumCover(db, album, third))
	require.Equal(t, third, cover(album))

	// A photo from outside the album is refused and changes nothing.
	err = SetAlbumCover(db, album, outside)
	require.True(t, errors.Is(err, ErrNotInAlbum), "cover from outside the album: %v", err)
	require.Equal(t, third, cover(album))

	// Taking the cover out of the album falls back to the first photo, and
	// the choice does not come back if the photo is added again.
	require.NoError(t, RemoveMediaFromAlbum(db, album, third))
	require.Equal(t, first, cover(album))
	require.NoError(t, AddMediaToAlbum(db, album, third))
	require.Equal(t, first, cover(album))

	// Deleting the cover photo from the gallery does the same.
	require.NoError(t, SetAlbumCover(db, album, second))
	_, err = db.Exec(`DELETE FROM media WHERE id = ?`, second)
	require.NoError(t, err)
	require.Equal(t, first, cover(album))

	// Removing an ordinary photo leaves the chosen cover alone.
	require.NoError(t, SetAlbumCover(db, album, third))
	require.NoError(t, RemoveMediaFromAlbum(db, album, first))
	require.Equal(t, third, cover(album))

	// A cover left pointing outside the album by an older version is
	// cleared when the migrations run.
	_, err = db.Exec(`UPDATE albums SET cover_media_id = ? WHERE id = ?`, outside, album)
	require.NoError(t, err)
	require.Equal(t, outside, cover(album))
	require.NoError(t, migrate.Migrate(db, sqlFS))
	require.Equal(t, third, cover(album))
}
