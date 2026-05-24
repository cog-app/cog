package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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

// Create generates a password reset token for the user. Invalidates existing
// tokens for the user. Returns the plaintext token; only the hash is stored.
func (s *PasswordResetStore) Create(userID string) (string, error) {
	// Invalidate existing tokens for this user — best-effort, log on failure.
	if _, err := s.db.Exec(`DELETE FROM password_reset_tokens WHERE user_id = ?`, userID); err != nil {
		slog.Warn("password reset invalidate previous", "user", userID, "err", err)
	}
	// Lazy cleanup expired tokens.
	if _, err := s.db.Exec(`DELETE FROM password_reset_tokens WHERE expires_at < ?`, s.clock().Unix()); err != nil {
		slog.Warn("password reset cleanup expired", "err", err)
	}

	token, err := generateToken(32)
	if err != nil {
		return "", err
	}

	now := s.clock()
	expires := now.Add(passwordResetDuration)

	_, err = s.db.Exec(
		`INSERT INTO password_reset_tokens (token, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		s.hash(token), userID, now.Unix(), expires.Unix(),
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
		`SELECT user_id, expires_at, used FROM password_reset_tokens WHERE token = ?`, s.hash(token),
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
	_, err := s.db.Exec(`UPDATE password_reset_tokens SET used = 1 WHERE token = ?`, s.hash(token))
	return err
}
