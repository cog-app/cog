package auth

import (
	"crypto/rand"
	"database/sql"
	"fmt"
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
// unused codes for the user in the same transaction as the insert, so a new
// code is only handed out once the old ones have stopped working. Returns the
// plaintext code.
func (s *EmailOTPStore) Create(userID string) (string, error) {
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
	now := s.clock()
	expires := now.Add(emailOTPDuration)

	tx, err := s.db.Begin()
	if err != nil {
		return "", fmt.Errorf("create email otp: begin: %w", err)
	}
	defer tx.Rollback()

	// The user's unused codes, and anyone's expired ones.
	if _, err := tx.Exec(`DELETE FROM email_otp WHERE (user_id = ? AND used = 0) OR expires_at < ?`, userID, now.Unix()); err != nil {
		return "", fmt.Errorf("invalidate previous email otp: %w", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO email_otp (id, user_id, code_hash, expires_at) VALUES (?, ?, ?, ?)`,
		id, userID, string(hash), expires.Unix(),
	); err != nil {
		return "", fmt.Errorf("insert email otp: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("create email otp: commit: %w", err)
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
	// used = 0 in the WHERE makes the claim atomic: of two requests racing
	// with the same code, only one changes the row.
	res, err := s.db.Exec(`UPDATE email_otp SET used = 1 WHERE id = ? AND used = 0`, matched)
	if err != nil {
		return false, fmt.Errorf("mark email otp used: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n == 1, nil
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
