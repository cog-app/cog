// Package keys provides CRUD operations for SSH public keys stored in per-user databases.
package keys

import (
	"database/sql"
	"fmt"
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
func migrateFingerprint(db *sql.DB) error {
	// Check if fingerprint column exists.
	var count int
	err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ssh_keys') WHERE name = 'fingerprint'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("check fingerprint column: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := db.Exec(`ALTER TABLE ssh_keys ADD COLUMN fingerprint TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("add fingerprint column: %w", err)
	}

	// Backfill fingerprints for existing keys.
	rows, err := db.Query(`SELECT id, public_key FROM ssh_keys WHERE fingerprint = ''`)
	if err != nil {
		return fmt.Errorf("query keys for backfill: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, pubKey string
		if err := rows.Scan(&id, &pubKey); err != nil {
			return fmt.Errorf("scan key for backfill: %w", err)
		}
		pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(pubKey))
		if err != nil {
			continue // skip unparseable keys
		}
		fp := gossh.FingerprintSHA256(pk)
		if _, err := db.Exec(`UPDATE ssh_keys SET fingerprint = ? WHERE id = ?`, fp, id); err != nil {
			return fmt.Errorf("backfill fingerprint: %w", err)
		}
	}

	// Create unique index on fingerprint (exclude empty strings from old broken keys).
	_, err = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_fingerprint ON ssh_keys(fingerprint) WHERE fingerprint != ''`)
	return err
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
	n, _ := result.RowsAffected()
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
		db, err := s.dbProvider.UserDB(username)
		if err != nil {
			continue
		}
		if err := migrateFingerprint(db); err != nil {
			continue
		}

		var k SSHKey
		var ts int64
		err = db.QueryRow(
			`SELECT id, name, public_key, fingerprint, created_at FROM ssh_keys WHERE fingerprint = ?`,
			fingerprint,
		).Scan(&k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &ts)
		if err != nil {
			continue
		}
		k.CreatedAt = time.Unix(ts, 0)
		return username, &k, nil
	}

	return "", nil, fmt.Errorf("no key found with fingerprint %s", fingerprint)
}
