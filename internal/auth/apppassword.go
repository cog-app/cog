package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

type AppPassword struct {
	ID        string
	UserID    string
	Name      string
	TokenHash string
	CreatedAt time.Time
}

// MaxAppPasswordsPerUser caps how many app passwords a single user can have
// outstanding. Beyond this limit, Create returns ErrTooManyAppPasswords and
// the user must delete an existing one.
const MaxAppPasswordsPerUser = 32

// ErrTooManyAppPasswords is returned by Create when a user already has
// MaxAppPasswordsPerUser tokens.
var ErrTooManyAppPasswords = errors.New("too many app passwords for user")

// ErrInvalidAppPassword is returned by Validate when no app password matches.
var ErrInvalidAppPassword = errors.New("invalid app password")

// ErrAppPasswordNotFound is returned by Delete when the user has no app
// password with that id.
var ErrAppPasswordNotFound = errors.New("app password not found")

// appPasswordSep separates the public id from the secret in a token. It must
// not be ':' (Basic auth splits on it) and must not occur in either half.
const appPasswordSep = "."

// An app password token is "<id>.<secret>": id is the row's primary key and
// secret is 32 random bytes, hex encoded. The id makes validation one
// indexed lookup; only SHA-256(secret) is stored. A fast hash is enough
// because the secret is 256 bits of randomness, not something a person
// chose, so there is nothing for a slow hash to protect against guessing.
func hashAppPasswordSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// splitAppPassword returns the id and secret of a token, or ok=false if the
// token is not in the "<id>.<secret>" form.
func splitAppPassword(token string) (id, secret string, ok bool) {
	id, secret, ok = strings.Cut(token, appPasswordSep)
	if !ok || id == "" || secret == "" {
		return "", "", false
	}
	return id, secret, true
}

type AppPasswordStore struct {
	db    *sql.DB
	clock clock.Clock
}

func NewAppPasswordStore(db *sql.DB, c clock.Clock) *AppPasswordStore {
	return &AppPasswordStore{db: db, clock: c}
}

// Create generates a new app password and returns the plaintext token, in
// the form "<id>.<secret>". Only a hash of the secret is stored; the caller must present the plaintext to the user.
// Refuses creation once the user is at the per-user cap.
func (s *AppPasswordStore) Create(userID, name string) (plaintext string, ap *AppPassword, err error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM app_passwords WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return "", nil, fmt.Errorf("count app passwords: %w", err)
	}
	if count >= MaxAppPasswordsPerUser {
		return "", nil, ErrTooManyAppPasswords
	}

	secret, err := generateToken(32)
	if err != nil {
		return "", nil, err
	}
	hash := hashAppPasswordSecret(secret)

	id, err := generateToken(16)
	if err != nil {
		return "", nil, err
	}
	token := id + appPasswordSep + secret

	now := s.clock()
	_, err = s.db.Exec(
		`INSERT INTO app_passwords (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, name, hash, now.Unix(),
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert app password: %w", err)
	}

	return token, &AppPassword{
		ID:        id,
		UserID:    userID,
		Name:      name,
		TokenHash: hash,
		CreatedAt: now,
	}, nil
}

// ListByUser returns all app passwords for a user, ordered by creation time descending.
func (s *AppPasswordStore) ListByUser(userID string) ([]AppPassword, error) {
	rows, err := s.db.Query(
		`SELECT id, user_id, name, token_hash, created_at FROM app_passwords WHERE user_id = ? ORDER BY created_at DESC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list app passwords: %w", err)
	}
	defer rows.Close()

	var results []AppPassword
	for rows.Next() {
		var ap AppPassword
		var ts int64
		if err := rows.Scan(&ap.ID, &ap.UserID, &ap.Name, &ap.TokenHash, &ts); err != nil {
			return nil, fmt.Errorf("scan app password: %w", err)
		}
		ap.CreatedAt = time.Unix(ts, 0)
		results = append(results, ap)
	}
	return results, rows.Err()
}

// Delete removes an app password by ID, scoped to a user.
func (s *AppPasswordStore) Delete(id, userID string) error {
	res, err := s.db.Exec(`DELETE FROM app_passwords WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete app password: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return ErrAppPasswordNotFound
	}
	return nil
}

// Validate checks a plaintext token and returns the owning user ID if valid.
// The token's id selects at most one row by primary key, and its secret is
// compared against that row's hash in constant time.
func (s *AppPasswordStore) Validate(token string) (userID string, err error) {
	id, secret, ok := splitAppPassword(token)
	if !ok {
		return "", ErrInvalidAppPassword
	}
	var uid, stored string
	err = s.db.QueryRow(`SELECT user_id, token_hash FROM app_passwords WHERE id = ?`, id).Scan(&uid, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidAppPassword
	}
	if err != nil {
		return "", fmt.Errorf("look up app password: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(hashAppPasswordSecret(secret)), []byte(stored)) != 1 {
		return "", ErrInvalidAppPassword
	}
	return uid, nil
}
