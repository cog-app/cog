package db

import (
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/migrate"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed sql
var sqlFS embed.FS

// Synchronous is SQLite's synchronous setting for every database the server
// opens. In WAL mode FULL syncs the log to disk at every commit; NORMAL
// syncs only at checkpoints, so a commit can be lost to a power cut or an
// operating system crash, though never to the process dying, and the
// database is never corrupted. NORMAL is what SQLite recommends with WAL,
// and the server commits often enough that a sync at each one was most of
// the time a write took.
//
// It is a variable so cogtest can relax it: a test environment commits a
// few dozen migrations before its first request, and waiting for the disk
// at each one was most of a typical test's time.
var Synchronous = "NORMAL"

// OpenSystem opens the system SQLite database and runs migrations.
// It enables WAL mode and returns a single connection for the
// single-writer pattern described in the design.
func OpenSystem(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on&_busy_timeout=5000&_synchronous="+Synchronous+"&"+TxLockImmediate)
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}
	TunePool(db, path)

	if err := Migrate(db); err != nil {
		return nil, errors.Join(fmt.Errorf("migrate system db: %w", err), db.Close())
	}

	return db, nil
}

// Migrate runs all schema migrations on the system database.
func Migrate(db *sql.DB) error {
	return migrate.Migrate(db, sqlFS)
}
