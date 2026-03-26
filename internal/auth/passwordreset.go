package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

const passwordResetDuration = 1 * time.Hour

// PasswordResetStore manages password reset tokens.
type PasswordResetStore struct {
	db    *sql.DB
	clock clock.Clock
}

// NewPasswordResetStore creates a new password reset store.
func NewPasswordResetStore(db *sql.DB, c clock.Clock) *PasswordResetStore {
	return &PasswordResetStore{db: db, clock: c}
}

// Create generates a password reset token for the user. Invalidates existing
// tokens for the user. Returns the plaintext token.
func (s *PasswordResetStore) Create(userID string) (string, error) {
	// Invalidate existing tokens for this user.
	s.db.Exec(`DELETE FROM password_reset_tokens WHERE user_id = ?`, userID)
	// Lazy cleanup expired tokens.
	s.db.Exec(`DELETE FROM password_reset_tokens WHERE expires_at < ?`, s.clock().Unix())

	token, err := generateToken(32)
	if err != nil {
		return "", err
	}

	now := s.clock()
	expires := now.Add(passwordResetDuration)

	_, err = s.db.Exec(
		`INSERT INTO password_reset_tokens (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, userID, now.Unix(), expires.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("create password reset: %w", err)
	}

	return token, nil
}

// Validate checks the token and returns the user ID if valid.
func (s *PasswordResetStore) Validate(token string) (string, error) {
	var userID string
	var expiresAt int64
	var used int
	err := s.db.QueryRow(
		`SELECT user_id, expires_at, used FROM password_reset_tokens WHERE token = ?`, token,
	).Scan(&userID, &expiresAt, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("invalid or expired reset token")
	}
	if err != nil {
		return "", fmt.Errorf("validate reset token: %w", err)
	}

	if used != 0 {
		return "", fmt.Errorf("reset token already used")
	}
	if time.Unix(expiresAt, 0).Before(s.clock()) {
		return "", fmt.Errorf("reset token expired")
	}

	return userID, nil
}

// MarkUsed marks a token as used.
func (s *PasswordResetStore) MarkUsed(token string) error {
	_, err := s.db.Exec(`UPDATE password_reset_tokens SET used = 1 WHERE token = ?`, token)
	return err
}
