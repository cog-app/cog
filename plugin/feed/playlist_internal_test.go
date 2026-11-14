package feed

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// A playlist from before playlists had an order plays newest first, as it
// always has, after the migration.
func TestPlaylistPlayMigration(t *testing.T) {
	s := newStoreInternal(t)
	_, err := s.db.Exec(`DROP TABLE playlists`)
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TABLE playlists (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL UNIQUE,
		query TEXT NOT NULL,
		sort_order INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL DEFAULT (unixepoch()))`)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO playlists (name, query) VALUES ('Old', 'type:audio')`)
	require.NoError(t, err)

	require.NoError(t, migrate.Migrate(s.db, sqlFS))
	require.NoError(t, migrate.Migrate(s.db, sqlFS)) // and again, as every open does

	pls, err := s.ListPlaylists()
	require.NoError(t, err)
	require.Equal(t, 1, len(pls))
	assert.Equal(t, OrderNewest, pls[0].Order)
	assert.False(t, pls[0].ListenOnly)
}
