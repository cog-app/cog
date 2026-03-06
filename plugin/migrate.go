package plugin

import (
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"
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
	schema, err := fs.ReadFile(fsys, "sql/schema.sql")
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		return fmt.Errorf("exec schema.sql: %w", err)
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
			exists, err := columnExists(db, table, col)
			if err != nil {
				return fmt.Errorf("%s: check column %s.%s: %w", path, table, col, err)
			}
			if exists {
				continue
			}
		}

		if _, err := db.Exec(body); err != nil {
			return fmt.Errorf("exec %s: %w", path, err)
		}
	}
	return nil
}

// parseColumnGuard looks for "-- IF COLUMN table.column NOT EXIST" in the SQL text.
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

func columnExists(db *sql.DB, table, column string) (bool, error) {
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
