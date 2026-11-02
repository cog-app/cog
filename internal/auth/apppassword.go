package auth

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"golang.org/x/crypto/bcrypt"
)

type AppPassword struct {
	ID        string
	UserID    string
	Name      string
	TokenHash string
	CreatedAt time.Time
}

// MaxAppPasswordsPerUser caps how many app passwords a single user can have
// outstanding. Validate iterates every app_passwords row and runs bcrypt
// against each, so an unbounded count is a DoS surface (audit security.md
// #11). Beyond this limit, Create returns ErrTooManyAppPasswords and the
// user must delete an existing one.
const MaxAppPasswordsPerUser = 32

// ErrTooManyAppPasswords is returned by Create when a user already has
// MaxAppPasswordsPerUser tokens.
var ErrTooManyAppPasswords = errors.New("too many app passwords for user")

// ErrInvalidAppPassword is returned by Validate when no app password matches.
var ErrInvalidAppPassword = errors.New("invalid app password")

type AppPasswordStore struct {
	db    *sql.DB
	clock clock.Clock
}

func NewAppPasswordStore(db *sql.DB, c clock.Clock) *AppPasswordStore {
	return &AppPasswordStore{db: db, clock: c}
}

// Create generates a new app password and returns the plaintext token.
// The token is hashed before storage; the caller must present the plaintext to the user.
// Refuses creation once the user is at the per-user cap.
func (s *AppPasswordStore) Create(userID, name string) (plaintext string, ap *AppPassword, err error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM app_passwords WHERE user_id = ?`, userID).Scan(&count); err != nil {
		return "", nil, fmt.Errorf("count app passwords: %w", err)
	}
	if count >= MaxAppPasswordsPerUser {
		return "", nil, ErrTooManyAppPasswords
	}

	token, err := generateToken(32)
	if err != nil {
		return "", nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(token), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, fmt.Errorf("hash app password: %w", err)
	}

	id, err := generateToken(16)
	if err != nil {
		return "", nil, err
	}

	now := s.clock()
	_, err = s.db.Exec(
		`INSERT INTO app_passwords (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, name, string(hash), now.Unix(),
	)
	if err != nil {
		return "", nil, fmt.Errorf("insert app password: %w", err)
	}

	return token, &AppPassword{
		ID:        id,
		UserID:    userID,
		Name:      name,
		TokenHash: string(hash),
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
		return errors.New("app password not found")
	}
	return nil
}

// Validate checks a plaintext token against all app passwords and returns the
// owning user ID if valid. It iterates all candidates to prevent timing leaks.
func (s *AppPasswordStore) Validate(token string) (userID string, err error) {
	rows, err := s.db.Query(`SELECT id, user_id, token_hash FROM app_passwords`)
	if err != nil {
		return "", fmt.Errorf("query app passwords: %w", err)
	}
	defer rows.Close()

	var matchedUID string
	matched := 0
	for rows.Next() {
		var id, uid, hash string
		if err := rows.Scan(&id, &uid, &hash); err != nil {
			return "", fmt.Errorf("scan app password: %w", err)
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(token)) == nil {
			matchedUID = uid
			matched = 1
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if subtle.ConstantTimeEq(int32(matched), 1) == 1 {
		return matchedUID, nil
	}
	return "", ErrInvalidAppPassword
}
