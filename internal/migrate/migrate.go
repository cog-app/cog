// Package migrate provides schema migration for SQLite databases using embedded SQL files.
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
package migrate

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Migrate runs schema creation and migrations from an embedded filesystem.
func Migrate(db *sql.DB, fsys fs.FS) error {
	schema, err := fs.ReadFile(fsys, "sql/schema.sql")
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}
	if err := execTx(db, string(schema)); err != nil {
		if _, ok := errors.AsType[scriptError](err); ok {
			return fmt.Errorf("%w: schema.sql: %w", ErrIncompatible, err)
		}
		return fmt.Errorf("schema.sql: %w", err)
	}

	entries, err := fs.Glob(fsys, "sql/migration*.sql")
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(entries)

	for _, path := range entries {
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		body := string(data)

		table, col, hasGuard := parseColumnGuard(body)
		if hasGuard {
			exists, err := ColumnExists(db, table, col)
			if err != nil {
				return fmt.Errorf("%s: check column %s.%s: %w", path, table, col, err)
			}
			if exists {
				continue
			}
		}

		if err := execTx(db, body); err != nil {
			return fmt.Errorf("exec %s: %w", path, err)
		}
	}
	return nil
}

// parseColumnGuard looks for "-- IF COLUMN table.column NOT EXIST" in the SQL text.
// ErrIncompatible is a database whose tables the current schema cannot be
// applied to: one made by a version with a different schema, such as a
// calendar, tasks or contacts database from before the DAV storage rewrite
// (DESIGN/dav-storage.md section 8). Nothing has been changed in it.
var ErrIncompatible = errors.New("the database was made by an incompatible version of Cog; move it aside and start from an empty one")

// scriptError is a statement of a script that failed, as opposed to the
// transaction around it.
type scriptError struct{ err error }

func (e scriptError) Error() string { return e.err.Error() }
func (e scriptError) Unwrap() error { return e.err }

// execTx runs a script in one transaction, so a script that fails part way
// leaves the database as it was rather than half changed.
func execTx(db *sql.DB, script string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(script); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			return errors.Join(scriptError{err}, fmt.Errorf("rollback: %w", rerr))
		}
		return scriptError{err}
	}
	return tx.Commit()
}

func parseColumnGuard(sql string) (table, column string, ok bool) {
	const prefix = "-- IF COLUMN "
	const suffix = " NOT EXIST"
	for line := range strings.SplitSeq(sql, "\n") {
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

// ColumnExists checks if a column exists on a table via PRAGMA table_info.
func ColumnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
