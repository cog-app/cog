// Package keys provides CRUD operations for SSH public keys, stored in the
// system database's ssh_keys table.
package keys

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"github.com/google/uuid"
	"github.com/mattn/go-sqlite3"
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

// ErrNotFound is returned when no key matches.
var ErrNotFound = errors.New("ssh key not found")

// ErrKeyInUse is returned by Add when the key is already registered, to this
// or another user. A fingerprint identifies exactly one user at login.
var ErrKeyInUse = errors.New("ssh key already registered")

// ErrInvalidKey is returned by Add for text that is not one public key in
// authorized_keys format.
var ErrInvalidKey = errors.New("not a valid ssh public key")

// LegacyDBProvider opens the per-user databases keys used to be kept in. It
// is only read once, to move those keys into the system database.
type LegacyDBProvider interface {
	UserDB(username string) (*sql.DB, error)
}

// UsernameProvider lists all usernames in the system.
type UsernameProvider interface {
	Usernames() ([]string, error)
}

// Store manages SSH keys in the system database.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// movedSetting marks, in system_settings, that keys from the per-user
// databases have been copied into the system database.
const movedSetting = "ssh.keys_moved"

// NewStore returns a store over the system database. The first time it runs
// against a database it copies keys from the per-user databases they used
// to live in; that must succeed, or users would silently lose their keys.
func NewStore(sysDB *sql.DB, legacy LegacyDBProvider, users UsernameProvider, c clock.Clock) (*Store, error) {
	s := &Store{db: sysDB, clock: c}
	if err := s.moveLegacy(legacy, users); err != nil {
		return nil, fmt.Errorf("move ssh keys to system database: %w", err)
	}
	return s, nil
}

type legacyKey struct {
	username, id, name, pubKey string
	createdAt                  int64
}

// moveLegacy copies every user's per-user ssh_keys rows into the system
// table, once. The copy and the marker commit together, so a failure part way
// leaves nothing behind and the next start tries again.
func (s *Store) moveLegacy(legacy LegacyDBProvider, users UsernameProvider) error {
	var done int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM system_settings WHERE key = ?`, movedSetting).Scan(&done); err != nil {
		return fmt.Errorf("check marker: %w", err)
	}
	if done > 0 {
		return nil
	}

	usernames, err := users.Usernames()
	if err != nil {
		return fmt.Errorf("list usernames: %w", err)
	}
	var pending []legacyKey
	for _, username := range usernames {
		ks, err := readLegacy(legacy, username)
		if err != nil {
			return fmt.Errorf("user %q: %w", username, err)
		}
		pending = append(pending, ks...)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	for _, k := range pending {
		pk, _, _, _, err := gossh.ParseAuthorizedKey([]byte(k.pubKey))
		if err != nil {
			// It could never authenticate; there is nothing to move.
			slog.Warn("ssh key move: unparsable key dropped", "user", k.username, "id", k.id, "err", err)
			continue
		}
		res, err := tx.Exec(
			`INSERT INTO ssh_keys (id, user_id, name, public_key, fingerprint, created_at)
			 SELECT ?, id, ?, ?, ?, ? FROM users WHERE username = ?
			 ON CONFLICT DO NOTHING`,
			k.id, k.name, k.pubKey, gossh.FingerprintSHA256(pk), k.createdAt, k.username,
		)
		if err != nil {
			return fmt.Errorf("insert key %s of %q: %w", k.id, k.username, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rows affected: %w", err)
		}
		if n == 0 {
			// The same key under two users: the old per-user lookup only ever
			// let one of them log in with it, and now only the first keeps it.
			slog.Warn("ssh key move: key already registered to another user, dropped", "user", k.username, "id", k.id, "fingerprint", gossh.FingerprintSHA256(pk))
		}
	}
	if _, err := tx.Exec(`INSERT INTO system_settings (key, value) VALUES (?, '1') ON CONFLICT DO NOTHING`, movedSetting); err != nil {
		return fmt.Errorf("set marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// readLegacy returns the keys in a user's per-user database, if it has an
// ssh_keys table.
func readLegacy(legacy LegacyDBProvider, username string) ([]legacyKey, error) {
	db, err := legacy.UserDB(username)
	if err != nil {
		return nil, fmt.Errorf("open user db: %w", err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'ssh_keys'`).Scan(&tables); err != nil {
		return nil, fmt.Errorf("check ssh_keys table: %w", err)
	}
	if tables == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT id, name, public_key, created_at FROM ssh_keys`)
	if err != nil {
		return nil, fmt.Errorf("read ssh keys: %w", err)
	}
	defer rows.Close()
	var out []legacyKey
	for rows.Next() {
		k := legacyKey{username: username}
		if err := rows.Scan(&k.id, &k.name, &k.pubKey, &k.createdAt); err != nil {
			return nil, fmt.Errorf("scan ssh key: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read ssh keys: %w", err)
	}
	return out, nil
}

// Add stores a new SSH key for a user, parsing the authorized_keys line.
// It returns ErrKeyInUse if any user already has the key.
func (s *Store) Add(username, name, authorizedKeyLine string) (*SSHKey, error) {
	pk, comment, _, rest, err := gossh.ParseAuthorizedKey([]byte(authorizedKeyLine))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidKey, err)
	}
	if len(bytes.TrimSpace(rest)) > 0 {
		return nil, fmt.Errorf("%w: more than one key", ErrInvalidKey)
	}
	if name == "" {
		name = comment
	}

	key := &SSHKey{
		ID:          uuid.NewString(),
		Name:        name,
		PublicKey:   authorizedKeyLine,
		Fingerprint: gossh.FingerprintSHA256(pk),
		CreatedAt:   s.clock(),
	}

	res, err := s.db.Exec(
		`INSERT INTO ssh_keys (id, user_id, name, public_key, fingerprint, created_at)
		 SELECT ?, id, ?, ?, ?, ? FROM users WHERE username = ?`,
		key.ID, key.Name, key.PublicKey, key.Fingerprint, key.CreatedAt.Unix(), username,
	)
	if se, ok := errors.AsType[sqlite3.Error](err); ok && se.ExtendedCode == sqlite3.ErrConstraintUnique {
		return nil, ErrKeyInUse
	}
	if err != nil {
		return nil, fmt.Errorf("insert ssh key: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("insert ssh key: rows affected: %w", err)
	}
	if n == 0 {
		return nil, fmt.Errorf("insert ssh key: no user %q", username)
	}
	return key, nil
}

// List returns all SSH keys for a user.
func (s *Store) List(username string) ([]SSHKey, error) {
	rows, err := s.db.Query(
		`SELECT k.id, k.name, k.public_key, k.fingerprint, k.created_at
		 FROM ssh_keys k JOIN users u ON u.id = k.user_id
		 WHERE u.username = ? ORDER BY k.created_at`,
		username,
	)
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

// Delete removes one of a user's SSH keys by ID.
func (s *Store) Delete(username, id string) error {
	result, err := s.db.Exec(
		`DELETE FROM ssh_keys WHERE id = ? AND user_id = (SELECT id FROM users WHERE username = ?)`,
		id, username,
	)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete ssh key: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("ssh key %q: %w", id, ErrNotFound)
	}
	return nil
}

// FindByFingerprint returns the username owning the key with the given
// SHA256 fingerprint, and the key. It returns ErrNotFound if none does.
func (s *Store) FindByFingerprint(fingerprint string) (string, *SSHKey, error) {
	var username string
	var k SSHKey
	var ts int64
	err := s.db.QueryRow(
		`SELECT u.username, k.id, k.name, k.public_key, k.fingerprint, k.created_at
		 FROM ssh_keys k JOIN users u ON u.id = k.user_id
		 WHERE k.fingerprint = ?`,
		fingerprint,
	).Scan(&username, &k.ID, &k.Name, &k.PublicKey, &k.Fingerprint, &ts)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, fmt.Errorf("fingerprint %s: %w", fingerprint, ErrNotFound)
	}
	if err != nil {
		return "", nil, fmt.Errorf("find ssh key: %w", err)
	}
	k.CreatedAt = time.Unix(ts, 0)
	return username, &k, nil
}
