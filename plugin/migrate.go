package plugin

import (
	"database/sql"
	"io/fs"

	"git.duckfam.us/jj-software/cog/internal/migrate"
)

// Migrate runs schema creation and migrations from an embedded filesystem.
//
// The filesystem should contain:
//   - sql/schema.sql: CREATE TABLE/INDEX IF NOT EXISTS statements (the full current schema)
//   - sql/migration*.sql: ordered migration files run after the schema
//
// Migration files may contain guard comments:
//
//	-- IF COLUMN table.column NOT EXIST
//
// When present, the migration only runs if the specified column does not exist.
func Migrate(db *sql.DB, fsys fs.FS) error {
	return migrate.Migrate(db, fsys)
}
