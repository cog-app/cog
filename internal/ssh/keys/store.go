// Package keys provides CRUD operations for SSH public keys stored in per-user databases.
package keys

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"github.com/google/uuid"
	gossh "golang.org/x/crypto/ssh"
)

// SSHKey represents a stored SSH public key.
type SSHKey struct {
	ID          string
	Name        string
	PublicKey   string // authorized_keys format
	Fingerprint string // SHA256 fingerprint
	CreatedAt   time.Time
}

// DBProvider provides access to per-user databases and the list of usernames.
type DBProvider interface {
	UserDB(username string) (*sql.DB, error)
}

// UsernameProvider lists all usernames in the system.
type UsernameProvider interface {
	Usernames() ([]string, error)
}

// Store manages SSH keys across per-user databases.
type Store struct {
	dbProvider DBProvider
	usernames  UsernameProvider
	clock      clock.Clock
}

// NewStore creates a new SSH key store.
func NewStore(dbp DBProvider, up UsernameProvider, c clock.Clock) *Store {
	return &Store{dbProvider: dbp, usernames: up, clock: c}
}

// migrateFingerprint adds the fingerprint column if missing and backfills it.
// It runs in one transaction: the column's presence is what marks the
// migration done, so a backfill that failed halfway must not leave it behind.
func migrateFingerprint(db *sql.DB) (err error) {
	// Check if fingerprint column exists.
	var count int
	err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('ssh_keys') WHERE name = 'fingerprint'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check fingerprint column: %w", err)
	}
	if count > 0 {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migrate fingerprint: begin: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`ALTER TABLE ssh_keys ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add fingerprint column: %w", err)
	}

	// Backfill fingerprints for existing keys. Read them all first: the
	// updates go through the same transaction.
	rows, err := tx.Query(`SELECT id, public_key FROM ssh_keys WHERE fingerprint = ''`)
	if err != nil {
		return fmt.Errorf("query keys for backfill: %w", err)
	}
	type keyRow struct{ id, pubKey string }
	var pending []keyRow
	for rows.Next() {
		var k keyRow
		if err := rows.Scan(&k.id, &k.pubKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan key for backfill: %w", err)
		}
		pending = append(pending, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("query keys for backfill: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("query keys for backfill: %w", err)
	}

	for _, k := range pending {
		pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(k.pubKey))
		if err != nil {
			// The key can never authenticate; leave it with no fingerprint.
			slog.Warn("ssh key backfill: unparsable key", "id", k.id, "err", err)
			continue
		}
		if _, err := tx.Exec(`UPDATE ssh_keys SET fingerprint = ? WHERE id = ?`, gossh.FingerprintSHA256(pk), k.id); err != nil {
			return fmt.Errorf("backfill fingerprint: %w", err)
		}
	}

	// Create unique index on fingerprint (exclude empty strings from old broken keys).
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_fingerprint ON ssh_keys(fingerprint) WHERE fingerprint != ''`); err != nil {
		return fmt.Errorf("create fingerprint index: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrate fingerprint: commit: %w", err)
	}
	return nil
}

// Add stores a new SSH key for a user, parsing the authorized_keys line.
func (s *Store) Add(username, name, authorizedKeyLine string) (*SSHKey, error) {
	pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(authorizedKeyLine))
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}

	db, err := s.dbProvider.UserDB(username)
	if err != nil {
		return nil, fmt.Errorf("open user db: %w", err)
	}
	if err := migrateFingerprint(db); err != nil {
		return nil, err
	}

	fp := gossh.FingerprintSHA256(pk)
	key := &SSHKey{
		ID:          uuid.NewString(),
		Name:        name,
		PublicKey:   authorizedKeyLine,
		Fingerprint: fp,
		CreatedAt:   s.clock(),
	}

	_, err = db.Exec(
		`INSERT INTO ssh_keys (id, name, public_key, fingerprint, created_at) VALUES (?, ?, ?, ?, ?)`,
		key.ID, key.Name, key.PublicKey, key.Fingerprint, key.CreatedAt.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("insert ssh key: %w", err)
	}
	return key, nil
}

// List returns all SSH keys for a user.
func (s *Store) List(username string) ([]SSHKey, error) {
	db, err := s.dbProvider.UserDB(username)
	if err != nil {
		return nil, fmt.Errorf("open user db: %w", err)
	}
	if err := migrateFingerprint(db); err != nil {
		return nil, err
	}

	rows, err := db.Query(`SELECT id, name, public_key, fingerprint, created_at FROM ssh_keys ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query ssh keys: %w", err)
	}
	defer rows.Close()

	var keys []SSHKey
	for rows.Next() {
		var k SSHKey
		var ts int64
		if err := rows.Scan(&k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &ts); err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		k.CreatedAt = time.Unix(ts, 0)
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// Delete removes an SSH key by ID.
func (s *Store) Delete(username, id string) error {
	db, err := s.dbProvider.UserDB(username)
	if err != nil {
		return fmt.Errorf("open user db: %w", err)
	}
	result, err := db.Exec(`DELETE FROM ssh_keys WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete ssh key: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("ssh key %q not found", id)
	}
	return nil
}

// FindByFingerprint searches all users' databases for a key matching the
// given SHA256 fingerprint. Returns the username and key if found.
func (s *Store) FindByFingerprint(fingerprint string) (string, *SSHKey, error) {
	usernames, err := s.usernames.Usernames()
	if err != nil {
		return "", nil, fmt.Errorf("list usernames: %w", err)
	}

	for _, username := range usernames {
		// One user's broken database must not lock everyone out, so a
		// failure here is logged and the search goes on.
		db, err := s.dbProvider.UserDB(username)
		if err != nil {
			slog.Error("find ssh key: open user db", "user", username, "err", err)
			continue
		}
		if err := migrateFingerprint(db); err != nil {
			slog.Error("find ssh key: migrate", "user", username, "err", err)
			continue
		}

		var k SSHKey
		var ts int64
		err = db.QueryRow(
			`SELECT id, name, public_key, fingerprint, created_at FROM ssh_keys WHERE fingerprint = ?`,
			fingerprint,
		).Scan(&k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &ts)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			slog.Error("find ssh key", "user", username, "err", err)
			continue
		}
		k.CreatedAt = time.Unix(ts, 0)
		return username, &k, nil
	}

	return "", nil, fmt.Errorf("no key found with fingerprint %s", fingerprint)
}
