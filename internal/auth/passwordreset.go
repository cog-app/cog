package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

const passwordResetDuration = 1 * time.Hour

const resetTokenLabel = "pwreset/v1"

// PasswordResetStore manages password reset tokens. Tokens are stored hashed
// (HMAC-SHA256 under a per-purpose key derived from the master secret) so a
// database leak does not yield usable reset tokens.
type PasswordResetStore struct {
	db     *sql.DB
	clock  clock.Clock
	secret []byte
}

// NewPasswordResetStore creates a new password reset store.
func NewPasswordResetStore(db *sql.DB, c clock.Clock, secret []byte) *PasswordResetStore {
	return &PasswordResetStore{db: db, clock: c, secret: secret}
}

func (s *PasswordResetStore) hash(token string) string {
	return HashAtRest(s.secret, resetTokenLabel, token)
}

// ErrInvalidResetToken is returned by Validate for a token that is unknown,
// already used or expired. Any other error is a failure to check it.
var ErrInvalidResetToken = errors.New("invalid or expired reset token")

// Create generates a password reset token for the user and invalidates the
// user's earlier tokens, in one transaction: a new link is only handed out
// once the old ones have stopped working. Returns the plaintext token; only
// the hash is stored.
func (s *PasswordResetStore) Create(userID string) (string, error) {
	token, err := generateToken(32)
	if err != nil {
		return "", err
	}
	now := s.clock()
	expires := now.Add(passwordResetDuration)

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("create password reset: begin: %w", err)
	}
	defer tx.Rollback()

	// The user's earlier tokens, and anyone's expired ones.
	if _, err := tx.Exec(`DELETE FROM password_reset_tokens WHERE user_id = ? OR expires_at < ?`, userID, now.Unix()); err != nil {
		return "", fmt.Errorf("invalidate previous password resets: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO password_reset_tokens (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		s.hash(token), userID, now.Unix(), expires.Unix(),
	); err != nil {
		return "", fmt.Errorf("create password reset: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("create password reset: commit: %w", err)
	}
	return token, nil
}

// Validate checks the token and returns the user ID if valid. It returns an
// error wrapping ErrInvalidResetToken if the token is unknown, used or
// expired.
func (s *PasswordResetStore) Validate(token string) (string, error) {
	var userID string
	var expiresAt int64
	var used int
	err := s.db.QueryRow(
		`SELECT user_id, expires_at, used FROM password_reset_tokens WHERE token = ?`, s.hash(token),
	).Scan(&userID, &expiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidResetToken
	}
	if err != nil {
		return "", fmt.Errorf("validate reset token: %w", err)
	}

	if used != 0 {
		return "", fmt.Errorf("already used: %w", ErrInvalidResetToken)
	}
	if time.Unix(expiresAt, 0).Before(s.clock()) {
		return "", fmt.Errorf("expired: %w", ErrInvalidResetToken)
	}

	return userID, nil
}

// Consume marks the token used and returns its user, if it is valid: known,
// unused and unexpired. Checking and marking are one statement, so of two
// requests racing with the same token only one gets the user.
func (s *PasswordResetStore) Consume(token string) (string, error) {
	var userID string
	err := s.db.QueryRow(
		`UPDATE password_reset_tokens SET used = 1 WHERE token = ? AND used = 0 AND expires_at >= ? RETURNING user_id`,
		s.hash(token), s.clock().Unix(),
	).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidResetToken
	}
	if err != nil {
		return "", fmt.Errorf("consume reset token: %w", err)
	}
	return userID, nil
}
