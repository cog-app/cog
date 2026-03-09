package db

import (
	"database/sql"
	"embed"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed sql/schema.sql sql/migration*.sql
var sqlFS embed.FS

// OpenSystem opens the system SQLite database and runs migrations.
// It enables WAL mode and returns a single connection for the
// single-writer pattern described in the design.
func OpenSystem(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}

	if err := Migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate system db: %w", err)
	}

	return db, nil
}

// Migrate runs all schema migrations on the system database.
func Migrate(db *sql.DB) error {
	return migrate.Migrate(db, sqlFS)
}
