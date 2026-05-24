package auth

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"golang.org/x/crypto/bcrypt"
)

const emailOTPDuration = 10 * time.Minute

// EmailOTPStore manages one-time email verification codes.
type EmailOTPStore struct {
	db    *sql.DB
	clock clock.Clock
}

// NewEmailOTPStore creates a new email OTP store.
func NewEmailOTPStore(db *sql.DB, c clock.Clock) *EmailOTPStore {
	return &EmailOTPStore{db: db, clock: c}
}

// Create generates a 6-digit OTP for the user. Invalidates any existing
// unused codes for the user. Returns the plaintext code.
func (s *EmailOTPStore) Create(userID string) (string, error) {
	// Lazy cleanup — best-effort but errors get logged so they're visible.
	if _, err := s.db.Exec(`DELETE FROM email_otp WHERE expires_at < ?`, s.clock().Unix()); err != nil {
		slog.Warn("email otp cleanup expired", "err", err)
	}
	if _, err := s.db.Exec(`DELETE FROM email_otp WHERE user_id = ? AND used = 0`, userID); err != nil {
		slog.Warn("email otp invalidate previous", "user", userID, "err", err)
	}

	code, err := generateOTPCode()
	if err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash otp: %w", err)
	}

	id, err := generateToken(16)
	if err != nil {
		return "", err
	}

	expires := s.clock().Add(emailOTPDuration)
	_, err = s.db.Exec(
		`INSERT INTO email_otp (id, user_id, code_hash, expires_at) VALUES (?, ?, ?, ?)`,
		id, userID, string(hash), expires.Unix(),
	)
	if err != nil {
		return "", fmt.Errorf("insert email otp: %w", err)
	}

	return code, nil
}

// Validate checks an OTP code for the user. Returns true if valid.
// Marks the code as used on success.
func (s *EmailOTPStore) Validate(userID, code string) (bool, error) {
	rows, err := s.db.Query(
		`SELECT id, code_hash FROM email_otp WHERE user_id = ? AND used = 0 AND expires_at > ?`,
		userID, s.clock().Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("query email otp: %w", err)
	}
	// Buffer the matched ID and close before UPDATE — issuing Exec while
	// the iterator is still open on the same SQLite connection silently
	// fails (same bug pattern as backup-code validation).
	var matched string
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan email otp: %w", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)); err == nil {
			matched = id
			break
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, err
	}
	rows.Close()
	if matched == "" {
		return false, nil
	}
	if _, err := s.db.Exec(`UPDATE email_otp SET used = 1 WHERE id = ?`, matched); err != nil {
		return false, fmt.Errorf("mark email otp used: %w", err)
	}
	return true, nil
}

// generateOTPCode returns a random 6-digit numeric code. crypto/rand failure
// surfaces as an error; the previous swallow path silently returned "000000"
// (audit security.md #29).
func generateOTPCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("generate otp code: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}
