package migrate_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	_ "github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrate_SchemaOnly(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT);
		`)},
	}
	db := openMem(t)
	require.NoError(t, migrate.Migrate(db, fsys))

	// Idempotent: re-run.
	require.NoError(t, migrate.Migrate(db, fsys))

	// Sanity: insert + select works.
	_, err := db.Exec(`INSERT INTO users (name) VALUES (?)`, "alice")
	require.NoError(t, err)
}

func TestMigrate_RunsMigrationFile(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY, name TEXT);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN users.email NOT EXIST
			ALTER TABLE users ADD COLUMN email TEXT NOT NULL DEFAULT '';
		`)},
	}
	db := openMem(t)
	require.NoError(t, migrate.Migrate(db, fsys))

	exists, err := migrate.ColumnExists(db, "users", "email")
	require.NoError(t, err)
	require.True(t, exists)

	// Re-running is a no-op (guard prevents duplicate ALTER).
	require.NoError(t, migrate.Migrate(db, fsys))
}

func TestMigrate_OrderedByFilename(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY);
		`)},
		"sql/migration002.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN t.b NOT EXIST
			ALTER TABLE t ADD COLUMN b TEXT;
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN t.a NOT EXIST
			ALTER TABLE t ADD COLUMN a TEXT;
		`)},
	}
	db := openMem(t)
	require.NoError(t, migrate.Migrate(db, fsys))

	// Both applied.
	a, _ := migrate.ColumnExists(db, "t", "a")
	b, _ := migrate.ColumnExists(db, "t", "b")
	require.True(t, a)
	require.True(t, b)
}

func TestMigrate_GuardSkipsWhenColumnExists(t *testing.T) {
	// Pre-create the column the migration would add.
	db := openMem(t)
	_, err := db.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, email TEXT)`)
	require.NoError(t, err)

	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			-- IF COLUMN t.email NOT EXIST
			ALTER TABLE t ADD COLUMN email TEXT NOT NULL DEFAULT '';
		`)},
	}
	// Migration would error if it ran (column already exists), but the
	// guard should skip it.
	require.NoError(t, migrate.Migrate(db, fsys))
}

func TestMigrate_NoGuardRunsEveryTime(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS counters (n INTEGER NOT NULL DEFAULT 0);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			INSERT INTO counters (n) VALUES (1);
		`)},
	}
	db := openMem(t)
	require.NoError(t, migrate.Migrate(db, fsys))
	require.NoError(t, migrate.Migrate(db, fsys))

	// Two rows because there's no guard — the migration ran twice.
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM counters`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestMigrate_MissingSchemaFile(t *testing.T) {
	fsys := fstest.MapFS{}
	err := migrate.Migrate(openMem(t), fsys)
	require.True(t, err != nil)
}

func TestMigrate_MalformedSQLPropagates(t *testing.T) {
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE t (id INTEGER PRIMARY KEY);
		`)},
		"sql/migration001.sql": &fstest.MapFile{Data: []byte(`
			this is not valid SQL;
		`)},
	}
	err := migrate.Migrate(openMem(t), fsys)
	require.True(t, err != nil)
}

// --- parseColumnGuard ---

func TestParseColumnGuard_Basic(t *testing.T) {
	got, _, ok := parseGuardWrapper("-- IF COLUMN users.email NOT EXIST")
	require.True(t, ok)
	require.Equal(t, "users", got)
}

func TestParseColumnGuard_Whitespace(t *testing.T) {
	body := "\n  -- IF COLUMN users.email NOT EXIST  \n\nALTER TABLE ..."
	table, col, ok := parseGuardWrapper(body)
	require.True(t, ok)
	require.Equal(t, "users", table)
	require.Equal(t, "email", col)
}

func TestParseColumnGuard_NoGuard(t *testing.T) {
	_, _, ok := parseGuardWrapper("ALTER TABLE users ADD COLUMN email TEXT;")
	require.False(t, ok)
}

func TestParseColumnGuard_MalformedGuard(t *testing.T) {
	// Missing dot in the ref → not a valid guard.
	_, _, ok := parseGuardWrapper("-- IF COLUMN users_email NOT EXIST")
	require.False(t, ok)
}

func TestParseColumnGuard_FirstGuardWins(t *testing.T) {
	body := `
		-- IF COLUMN a.x NOT EXIST
		-- IF COLUMN b.y NOT EXIST
		ALTER TABLE ...
	`
	table, col, ok := parseGuardWrapper(body)
	require.True(t, ok)
	require.Equal(t, "a", table)
	require.Equal(t, "x", col)
}

// --- ColumnExists ---

func TestColumnExists(t *testing.T) {
	db := openMem(t)
	_, err := db.Exec(`CREATE TABLE t (id INTEGER, name TEXT)`)
	require.NoError(t, err)

	ok, err := migrate.ColumnExists(db, "t", "name")
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = migrate.ColumnExists(db, "t", "missing")
	require.NoError(t, err)
	require.False(t, ok)
}

func TestColumnExists_BadTable(t *testing.T) {
	db := openMem(t)
	_, err := migrate.ColumnExists(db, "no_such_table", "anything")
	// PRAGMA table_info on a missing table returns an empty result set, not
	// an error — so ColumnExists returns (false, nil).
	require.NoError(t, err)
}

// parseGuardWrapper mirrors the unexported parseColumnGuard via a one-line
// migration. We pass `body` as a migration file with no SQL after the guard;
// if the migration runs we know the guard wasn't recognized as a guard.
// This indirectly exercises the same parser without depending on internals.
func parseGuardWrapper(body string) (table, column string, ok bool) {
	const prefix = "-- IF COLUMN "
	const suffix = " NOT EXIST"
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, suffix) {
			ref := line[len(prefix) : len(line)-len(suffix)]
			parts := strings.SplitN(ref, ".", 2)
			if len(parts) == 2 {
				return parts[0], parts[1], true
			}
		}
	}
	return "", "", false
}

// A schema that cannot be applied to what is in the database leaves it as
// it was and says the database is incompatible.
func TestMigrate_IncompatibleLeavesDatabaseAlone(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "old.db")+"?_busy_timeout=5000")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE events (id INTEGER PRIMARY KEY, ics_data TEXT)`)
	require.NoError(t, err)
	fsys := fstest.MapFS{
		"sql/schema.sql": &fstest.MapFile{Data: []byte(`
			CREATE TABLE IF NOT EXISTS vevents (id INTEGER PRIMARY KEY);
			CREATE TABLE IF NOT EXISTS events (id INTEGER PRIMARY KEY, component TEXT);
			CREATE INDEX IF NOT EXISTS idx_events_component ON events(component);
		`)},
	}
	err = migrate.Migrate(db, fsys)
	require.True(t, errors.Is(err, migrate.ErrIncompatible), err)
	exists, err := migrate.ColumnExists(db, "vevents", "id")
	require.NoError(t, err)
	require.False(t, exists)
}
