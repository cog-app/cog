package gallery

import (
	"io/fs"
	"regexp"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestFreshSchemaHasMigrations checks that a new database ends up with every
// column and index the migrations add. A guarded migration is skipped
// entirely when schema.sql already has its column, so anything else in it
// must reach a fresh database another way; idx_media_fingerprint did not.
func TestFreshSchemaHasMigrations(t *testing.T) {
	db := testDB(t)

	addColumn := regexp.MustCompile(`(?i)ALTER TABLE (\w+) ADD COLUMN (\w+)`)
	createIndex := regexp.MustCompile(`(?i)CREATE (?:UNIQUE )?INDEX (?:IF NOT EXISTS )?(\w+)`)
	createTrigger := regexp.MustCompile(`(?i)CREATE TRIGGER (?:IF NOT EXISTS )?(\w+)`)
	files, err := fs.Glob(sqlFS, "sql/migration*.sql")
	require.NoError(t, err)
	for _, f := range files {
		body, err := fs.ReadFile(sqlFS, f)
		require.NoError(t, err)
		for _, m := range addColumn.FindAllStringSubmatch(string(body), -1) {
			ok, err := migrate.ColumnExists(db, m[1], m[2])
			require.NoError(t, err)
			assert.True(t, ok, "%s: column %s.%s missing from a fresh database", f, m[1], m[2])
		}
		for _, m := range createIndex.FindAllStringSubmatch(string(body), -1) {
			var n int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, m[1]).Scan(&n))
			assert.True(t, n == 1, "%s: index %s missing from a fresh database", f, m[1])
		}
		for _, m := range createTrigger.FindAllStringSubmatch(string(body), -1) {
			var n int
			require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ?`, m[1]).Scan(&n))
			assert.True(t, n == 1, "%s: trigger %s missing from a fresh database", f, m[1])
		}
	}
}
