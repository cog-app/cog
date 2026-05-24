package auth

import (
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"golang.org/x/crypto/bcrypt"
)

const totpEncryptLabel = "totp_secret/v1"

// TOTPStore manages TOTP secrets and backup codes.
type TOTPStore struct {
	db            *sql.DB
	encryptionKey []byte // 32-byte AES key, derived from master via HKDF
}

// NewTOTPStore creates a new TOTP store. secret is the master secret used to
// derive a TOTP-specific AES key. Different purposes (CSRF, webauthn session
// encryption, etc.) derive distinct keys so one compromise doesn't cascade.
func NewTOTPStore(db *sql.DB, secret []byte) *TOTPStore {
	return &TOTPStore{db: db, encryptionKey: DerivedKey(secret, totpEncryptLabel)}
}

// BeginSetup generates a new TOTP key and stores the encrypted secret
// in the users table without enabling MFA. Returns the OTP key for QR display.
func (s *TOTPStore) BeginSetup(userID, username, issuer string) (*otp.Key, error) {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      issuer,
		AccountName: username,
	})
	if err != nil {
		return nil, fmt.Errorf("generate totp: %w", err)
	}

	encrypted, err := encryptSecret([]byte(key.Secret()), s.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt totp secret: %w", err)
	}

	_, err = s.db.Exec(`UPDATE users SET totp_secret = ? WHERE id = ?`, encrypted, userID)
	if err != nil {
		return nil, fmt.Errorf("store totp secret: %w", err)
	}

	return key, nil
}

// ConfirmSetup verifies a TOTP code against the pending secret, sets
// mfa_method to "totp", and generates backup codes. Returns plaintext backup codes.
func (s *TOTPStore) ConfirmSetup(userID, code string) ([]string, error) {
	secret, err := s.decryptUserSecret(userID)
	if err != nil {
		return nil, err
	}
	if secret == "" {
		return nil, fmt.Errorf("no pending totp secret")
	}

	if !totp.Validate(code, secret) {
		return nil, fmt.Errorf("invalid totp code")
	}

	_, err = s.db.Exec(`UPDATE users SET mfa_method = 'totp' WHERE id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("enable totp: %w", err)
	}

	return s.regenerateBackupCodes(userID)
}

// Validate checks a TOTP code or backup code for the user.
func (s *TOTPStore) Validate(userID, code string) (bool, error) {
	// Try TOTP first.
	secret, err := s.decryptUserSecret(userID)
	if err != nil {
		return false, err
	}
	if secret != "" && totp.Validate(code, secret) {
		return true, nil
	}

	// Try backup codes.
	return s.validateBackupCode(userID, code)
}

// Disable removes TOTP configuration and all backup codes for the user.
func (s *TOTPStore) Disable(userID string) error {
	_, err := s.db.Exec(`UPDATE users SET totp_secret = '', mfa_method = '' WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	_, err = s.db.Exec(`DELETE FROM totp_backup_codes WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("delete backup codes: %w", err)
	}
	return nil
}

// RegenerateBackupCodes deletes existing codes and creates new ones.
// Returns the plaintext codes.
func (s *TOTPStore) RegenerateBackupCodes(userID string) ([]string, error) {
	return s.regenerateBackupCodes(userID)
}

// BackupCodeCount returns the number of unused backup codes for the user.
func (s *TOTPStore) BackupCodeCount(userID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM totp_backup_codes WHERE user_id = ? AND used = 0`, userID).Scan(&count)
	return count, err
}

func (s *TOTPStore) decryptUserSecret(userID string) (string, error) {
	var encrypted string
	err := s.db.QueryRow(`SELECT totp_secret FROM users WHERE id = ?`, userID).Scan(&encrypted)
	if err != nil {
		return "", fmt.Errorf("get totp secret: %w", err)
	}
	if encrypted == "" {
		return "", nil
	}
	plaintext, err := decryptSecret(encrypted, s.encryptionKey)
	if err != nil {
		return "", fmt.Errorf("decrypt totp secret: %w", err)
	}
	return string(plaintext), nil
}

func (s *TOTPStore) validateBackupCode(userID, code string) (bool, error) {
	code = strings.TrimSpace(strings.ToLower(code))
	rows, err := s.db.Query(`SELECT id, code_hash FROM totp_backup_codes WHERE user_id = ? AND used = 0`, userID)
	if err != nil {
		return false, fmt.Errorf("query backup codes: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			return false, fmt.Errorf("scan backup code: %w", err)
		}
		if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)); err == nil {
			// Mark as used.
			s.db.Exec(`UPDATE totp_backup_codes SET used = 1 WHERE id = ?`, id)
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *TOTPStore) regenerateBackupCodes(userID string) ([]string, error) {
	// Delete existing codes.
	if _, err := s.db.Exec(`DELETE FROM totp_backup_codes WHERE user_id = ?`, userID); err != nil {
		return nil, fmt.Errorf("delete old backup codes: %w", err)
	}

	const numCodes = 10
	codes := make([]string, numCodes)
	for i := range codes {
		codes[i] = generateBackupCode()
	}

	for _, code := range codes {
		id, err := generateToken(16)
		if err != nil {
			return nil, err
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(code), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("hash backup code: %w", err)
		}
		_, err = s.db.Exec(
			`INSERT INTO totp_backup_codes (id, user_id, code_hash) VALUES (?, ?, ?)`,
			id, userID, string(hash),
		)
		if err != nil {
			return nil, fmt.Errorf("insert backup code: %w", err)
		}
	}

	return codes, nil
}

// generateBackupCode returns an 8-character lowercase alphanumeric code.
// Uses an unambiguous alphabet (no 0/o/1/l/i).
func generateBackupCode() string {
	const alphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	b := make([]byte, 8)
	rand.Read(b)
	code := make([]byte, 8)
	for i := range code {
		code[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(code)
}
