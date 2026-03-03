package db

import (
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

// OpenSystem opens the system SQLite database and runs migrations.
// It enables WAL mode and returns a single connection for the
// single-writer pattern described in the design.
func OpenSystem(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open system db: %w", err)
	}

	// Single writer: limit to one open connection for writes.
	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate system db: %w", err)
	}

	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		password_hash TEXT NOT NULL,
		home_mount TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id),
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS app_passwords (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id),
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);

	CREATE TABLE IF NOT EXISTS groups (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		parent_group_id TEXT REFERENCES groups(id)
	);

	CREATE TABLE IF NOT EXISTS group_members (
		group_id TEXT NOT NULL REFERENCES groups(id),
		member_id TEXT NOT NULL,
		member_type TEXT NOT NULL CHECK(member_type IN ('USER', 'GROUP')),
		PRIMARY KEY (group_id, member_id)
	);

	CREATE TABLE IF NOT EXISTS shares (
		id TEXT PRIMARY KEY,
		owner_user_id TEXT NOT NULL REFERENCES users(id),
		resource_type TEXT NOT NULL,
		resource_id TEXT NOT NULL,
		subject_type TEXT NOT NULL CHECK(subject_type IN ('USER', 'GROUP', 'PUBLIC')),
		subject_id TEXT NOT NULL DEFAULT '',
		permissions INTEGER NOT NULL DEFAULT 1,
		token TEXT,
		password_hash TEXT,
		expiration INTEGER,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_shares_subject ON shares(subject_type, subject_id);
	CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner_user_id);
	CREATE INDEX IF NOT EXISTS idx_shares_resource ON shares(resource_type, resource_id);
	CREATE UNIQUE INDEX IF NOT EXISTS idx_shares_token ON shares(token) WHERE token IS NOT NULL;
	`
	_, err := db.Exec(schema)
	return err
}
