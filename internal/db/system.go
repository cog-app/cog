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
		created_at INTEGER NOT NULL,
		is_admin INTEGER NOT NULL DEFAULT 0,
		display_name TEXT NOT NULL DEFAULT ''
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

	CREATE TABLE IF NOT EXISTS volumes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL DEFAULT '',
		uri TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT 'system',
		created_at INTEGER NOT NULL,
		owner_id TEXT REFERENCES users(id),
		config TEXT NOT NULL DEFAULT '{}'
	);

	CREATE TABLE IF NOT EXISTS mounts (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL REFERENCES users(id),
		user_path TEXT NOT NULL,
		volume_id TEXT NOT NULL REFERENCES volumes(id),
		volume_subpath TEXT NOT NULL DEFAULT '',
		read_only INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL,
		UNIQUE(user_id, user_path)
	);
	CREATE INDEX IF NOT EXISTS idx_mounts_user ON mounts(user_id);
	CREATE INDEX IF NOT EXISTS idx_mounts_volume ON mounts(volume_id);

	CREATE TABLE IF NOT EXISTS plugin_settings (
		plugin_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 1
	);
	`
	if _, err := db.Exec(schema); err != nil {
		return err
	}

	// Add columns to existing databases (SQLite has no ALTER TABLE ADD COLUMN IF NOT EXISTS).
	type colMigration struct {
		table string
		name  string
		ddl   string
	}
	for _, col := range []colMigration{
		{"users", "is_admin", "ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0"},
		{"users", "display_name", "ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT ''"},
		{"volumes", "owner_id", "ALTER TABLE volumes ADD COLUMN owner_id TEXT REFERENCES users(id)"},
		{"volumes", "config", "ALTER TABLE volumes ADD COLUMN config TEXT NOT NULL DEFAULT '{}'"},
		{"users", "avatar", "ALTER TABLE users ADD COLUMN avatar TEXT NOT NULL DEFAULT ''"},
	} {
		rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", col.table))
		if err != nil {
			return fmt.Errorf("pragma table_info: %w", err)
		}
		found := false
		for rows.Next() {
			var cid int
			var name, typ string
			var notnull int
			var dflt sql.NullString
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == col.name {
				found = true
			}
		}
		rows.Close()
		if !found {
			if _, err := db.Exec(col.ddl); err != nil {
				return fmt.Errorf("add column %s: %w", col.name, err)
			}
		}
	}

	return nil
}
