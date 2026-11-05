package user

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

// An email address decides who receives invitations and password resets, so
// it belongs to one account and is confirmed before it counts: users.email
// holds only an address its owner proved by opening a link sent to it, or one
// an admin set.

// ErrEmailTaken is returned when another account has the address.
var ErrEmailTaken = errors.New("email address belongs to another account")

// ErrInvalidVerification is returned for an unknown, used or expired link.
var ErrInvalidVerification = errors.New("invalid or expired confirmation link")

// verificationLifetime is how long a confirmation link works.
const verificationLifetime = 24 * time.Hour

func hashVerification(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// isUniqueViolation reports whether err is a UNIQUE constraint failing.
func isUniqueViolation(err error) bool {
	var se sqlite3.Error
	return errors.As(err, &se) && (se.ExtendedCode == sqlite3.ErrConstraintUnique || se.ExtendedCode == sqlite3.ErrConstraintPrimaryKey)
}

// RequestEmail records that the user wants email as their address and
// returns the token for the link that confirms it. Their current address
// stays until then; an earlier request is replaced.
func (s *Store) RequestEmail(userID, email string) (string, error) {
	token, err := generateID()
	if err != nil {
		return "", err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM email_verifications WHERE user_id = ?`, userID); err != nil {
		return "", fmt.Errorf("replace email request: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO email_verifications (token_hash, user_id, email, expires_at) VALUES (?, ?, ?, ?)`,
		hashVerification(token), userID, strings.TrimSpace(email), s.clock().Add(verificationLifetime).Unix()); err != nil {
		return "", fmt.Errorf("record email request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit email request: %w", err)
	}
	return token, nil
}

// PendingEmail returns the address the user asked for and has not yet
// confirmed, or "".
func (s *Store) PendingEmail(userID string) (string, error) {
	var email string
	err := s.db.QueryRow(`SELECT email FROM email_verifications WHERE user_id = ? AND expires_at > ?`, userID, s.clock().Unix()).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("pending email: %w", err)
	}
	return email, nil
}

// ConfirmEmail makes the address a link was sent to the address of the
// user with userID, who must be the one who asked for it. The link works
// once.
func (s *Store) ConfirmEmail(userID, token string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	var email string
	err = tx.QueryRow(`DELETE FROM email_verifications WHERE token_hash = ? AND user_id = ? AND expires_at > ? RETURNING email`,
		hashVerification(token), userID, s.clock().Unix()).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrInvalidVerification
	}
	if err != nil {
		return "", fmt.Errorf("confirm email: %w", err)
	}
	if _, err := tx.Exec(`UPDATE users SET email = ? WHERE id = ?`, email, userID); err != nil {
		if isUniqueViolation(err) {
			return "", ErrEmailTaken
		}
		return "", fmt.Errorf("set email: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit email: %w", err)
	}
	return email, nil
}
