package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

const (
	MFAPendingCookieName = "cog_mfa_pending"
	mfaPendingDuration   = 5 * time.Minute
)

const mfaPendingLabel = "mfa_pending/v1"

// MFAPendingStore manages short-lived tokens for users who have passed
// password auth but still need to complete MFA verification. Tokens are stored
// hashed at rest.
type MFAPendingStore struct {
	db     *sql.DB
	clock  clock.Clock
	secret []byte
}

// MFAPending represents a pending MFA verification.
type MFAPending struct {
	Token   string
	UserID  string
	NextURL string
}

// NewMFAPendingStore creates a new MFA pending store.
func NewMFAPendingStore(db *sql.DB, c clock.Clock, secret []byte) *MFAPendingStore {
	return &MFAPendingStore{db: db, clock: c, secret: secret}
}

func (s *MFAPendingStore) hash(token string) string {
	return HashAtRest(s.secret, mfaPendingLabel, token)
}

// Create generates a pending MFA token for the user.
func (s *MFAPendingStore) Create(userID, nextURL string) (*MFAPending, error) {
	// Lazy cleanup of expired entries.
	s.db.Exec(`DELETE FROM mfa_pending WHERE expires_at < ?`, s.clock().Unix())

	token, err := generateToken(32)
	if err != nil {
		return nil, err
	}

	expires := s.clock().Add(mfaPendingDuration)
	_, err = s.db.Exec(
		`INSERT INTO mfa_pending (token, user_id, next_url, expires_at) VALUES (?, ?, ?, ?)`,
		s.hash(token), userID, nextURL, expires.Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("create mfa pending: %w", err)
	}

	return &MFAPending{Token: token, UserID: userID, NextURL: nextURL}, nil
}

// Validate checks the pending MFA token and returns it if valid.
// The returned Token field is the plaintext supplied by the caller (not the
// hash) so it can be re-used for subsequent Delete calls.
func (s *MFAPendingStore) Validate(token string) (*MFAPending, error) {
	var p MFAPending
	var expiresAt int64
	err := s.db.QueryRow(
		`SELECT user_id, next_url, expires_at FROM mfa_pending WHERE token = ?`, s.hash(token),
	).Scan(&p.UserID, &p.NextURL, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("validate mfa pending: %w", err)
	}

	if time.Unix(expiresAt, 0).Before(s.clock()) {
		s.db.Exec(`DELETE FROM mfa_pending WHERE token = ?`, s.hash(token))
		return nil, nil
	}

	p.Token = token
	return &p, nil
}

// Delete removes a pending MFA token.
func (s *MFAPendingStore) Delete(token string) error {
	_, err := s.db.Exec(`DELETE FROM mfa_pending WHERE token = ?`, s.hash(token))
	return err
}
