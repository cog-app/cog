// migratesql updates a plugin's sql/schema.sql by applying all migration*.sql
// files to an in-memory SQLite database and dumping the resulting schema.
//
// Usage: go tool migratesql <dir>
//
// <dir> is the directory containing sql/schema.sql and sql/migration*.sql.
// The tool reads the current schema, applies migrations, and rewrites schema.sql
// with the final DDL (using IF NOT EXISTS for all statements).
package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/plugin"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: migratesql <dir>")
		os.Exit(1)
	}
	dir := os.Args[1]
	if err := run(dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir string) error {
	sqlDir := filepath.Join(dir, "sql")
	schemaPath := filepath.Join(sqlDir, "schema.sql")

	old, err := os.ReadFile(schemaPath)
	if err != nil {
		return fmt.Errorf("read schema.sql: %w", err)
	}

	db, err := sql.Open("sqlite3", ":memory:?_foreign_keys=on")
	if err != nil {
		return err
	}
	defer db.Close()

	if err := plugin.Migrate(db, os.DirFS(dir)); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	rows, err := db.Query(`SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT GLOB '_cog_*' ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 ELSE 2 END, name`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var stmts []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return err
		}
		if strings.HasPrefix(s, "CREATE TABLE sqlite_") {
			continue
		}
		if strings.Contains(s, "sqlite_autoindex_") {
			continue
		}
		// SQLite strips IF NOT EXISTS from stored DDL; add it back.
		s = strings.Replace(s, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)
		s = strings.Replace(s, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ", 1)
		s = strings.Replace(s, "CREATE UNIQUE INDEX ", "CREATE UNIQUE INDEX IF NOT EXISTS ", 1)
		s = strings.Replace(s, "CREATE TRIGGER ", "CREATE TRIGGER IF NOT EXISTS ", 1)
		stmts = append(stmts, s+";\n")
	}
	if err := rows.Err(); err != nil {
		return err
	}

	newSchema := strings.Join(stmts, "\n")

	if string(old) == newSchema {
		return nil
	}

	if err := os.WriteFile(schemaPath, []byte(newSchema), 0644); err != nil {
		return fmt.Errorf("write schema.sql: %w", err)
	}
	fmt.Printf("updated %s\n", schemaPath)
	return nil
}
