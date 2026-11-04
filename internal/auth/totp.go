package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/user"

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

	// Enabling MFA and issuing its backup codes happen together: a user must
	// never have TOTP enabled with no way to recover from a lost device.
	return s.regenerateBackupCodes(userID, `UPDATE users SET mfa_method = 'totp' WHERE id = ?`)
}

// Validate checks a TOTP code or backup code for the user.
func (s *TOTPStore) Validate(userID, code string) (bool, error) {
	// Try TOTP first.
	secret, err := s.decryptUserSecret(userID)
	if err != nil {
		return false, err
	}
	if secret != "" {
		if ok, err := s.validateCode(userID, secret, code, time.Now()); ok || err != nil {
			return ok, err
		}
	}

	// Try backup codes.
	return s.validateBackupCode(userID, code)
}

// totpPeriod is the length of a TOTP time step, the authenticator apps'
// default.
const totpPeriod = 30

// validateCode checks a code against the steps either side of now, as
// totp.Validate does, and accepts it only if its step is later than the last
// accepted one. Recording the step and checking it are one statement, so two
// requests racing with the same code cannot both succeed.
func (s *TOTPStore) validateCode(userID, secret, code string, now time.Time) (bool, error) {
	step := now.Unix() / totpPeriod
	for _, st := range []int64{step - 1, step, step + 1} {
		want, err := totp.GenerateCodeCustom(secret, time.Unix(st*totpPeriod, 0), totp.ValidateOpts{
			Period: totpPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return false, fmt.Errorf("generate totp code: %w", err)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) != 1 {
			continue
		}
		res, err := s.db.Exec(`UPDATE users SET totp_last_step = ? WHERE id = ? AND totp_last_step < ?`, st, userID, st)
		if err != nil {
			return false, fmt.Errorf("record totp step: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("record totp step: %w", err)
		}
		return n == 1, nil
	}
	return false, nil
}

// Disable removes TOTP configuration and all backup codes for the user.
func (s *TOTPStore) Disable(userID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET totp_secret = '', mfa_method = '' WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("disable totp: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM totp_backup_codes WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete backup codes: %w", err)
	}
	return tx.Commit()
}

// RegenerateBackupCodes deletes existing codes and creates new ones.
// Returns the plaintext codes.
func (s *TOTPStore) RegenerateBackupCodes(userID string) ([]string, error) {
	return s.regenerateBackupCodes(userID, "")
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
	if errors.Is(err, sql.ErrNoRows) {
		return "", user.ErrNotFound
	}
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
	// Buffer the matching row ID and close the iterator before issuing the
	// UPDATE — running Exec while rows is open contends with the same
	// SQLite connection and the UPDATE silently fails (the audit flagged
	// this class of bug).
	var matched string
	for rows.Next() {
		var id, hash string
		if err := rows.Scan(&id, &hash); err != nil {
			rows.Close()
			return false, fmt.Errorf("scan backup code: %w", err)
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
	res, err := s.db.Exec(`UPDATE totp_backup_codes SET used = 1 WHERE id = ? AND used = 0`, matched)
	if err != nil {
		return false, fmt.Errorf("mark backup code used: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return n == 1, nil
}

// regenerateBackupCodes replaces the user's backup codes, and runs also (if
// not empty, with userID as its argument) in the same transaction. The codes
// are hashed first, so the transaction is not held open across bcrypt.
func (s *TOTPStore) regenerateBackupCodes(userID, also string) ([]string, error) {
	const numCodes = 10
	codes := make([]string, numCodes)
	ids := make([]string, numCodes)
	hashes := make([][]byte, numCodes)
	for i := range codes {
		c, err := generateBackupCode()
		if err != nil {
			return nil, err
		}
		codes[i] = c
		if ids[i], err = generateToken(16); err != nil {
			return nil, err
		}
		if hashes[i], err = bcrypt.GenerateFromPassword([]byte(c), bcrypt.DefaultCost); err != nil {
			return nil, fmt.Errorf("hash backup code: %w", err)
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if also != "" {
		if _, err := tx.Exec(also, userID); err != nil {
			return nil, fmt.Errorf("enable totp: %w", err)
		}
	}
	if _, err := tx.Exec(`DELETE FROM totp_backup_codes WHERE user_id = ?`, userID); err != nil {
		return nil, fmt.Errorf("delete old backup codes: %w", err)
	}
	for i := range codes {
		if _, err := tx.Exec(
			`INSERT INTO totp_backup_codes (id, user_id, code_hash) VALUES (?, ?, ?)`,
			ids[i], userID, string(hashes[i]),
		); err != nil {
			return nil, fmt.Errorf("insert backup code: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit backup codes: %w", err)
	}
	return codes, nil
}

// generateBackupCode returns an 8-character lowercase alphanumeric code.
// Uses an unambiguous alphabet (no 0/o/1/l/i). crypto/rand failure surfaces
// as an error; previous swallow could have produced predictable codes from
// a zero buffer (audit security.md #31).
func generateBackupCode() (string, error) {
	const alphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate backup code: %w", err)
	}
	code := make([]byte, 8)
	for i := range code {
		code[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(code), nil
}
