package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
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
// nextURL is sanitized server-side: only same-origin in-app paths survive;
// anything else (protocol-relative, absolute URL, javascript:, missing
// leading slash) is dropped on the floor. This defends against malformed
// values reaching the post-MFA redirect path (audit security.md #18) even
// if a later refactor forgets to call safeNext at read time.
func (s *MFAPendingStore) Create(userID, nextURL string) (*MFAPending, error) {
	nextURL = sanitizeNextURL(nextURL)

	// Lazy cleanup of expired entries — best-effort, log on failure.
	if _, err := s.db.Exec(`DELETE FROM mfa_pending WHERE expires_at < ?`, s.clock().Unix()); err != nil {
		slog.Warn("mfa pending cleanup expired", "err", err)
	}

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

// maxNextURLLen caps the post-MFA redirect path to avoid an unbounded
// write to mfa_pending.next_url (audit security.md #47). 2KB is well above
// any legitimate same-origin path.
const maxNextURLLen = 2048

// sanitizeNextURL returns next only when it is a same-origin in-app path
// (LocalPath) of a sane length. All other inputs return "" so the post-MFA
// redirect falls back to the root.
func sanitizeNextURL(next string) string {
	if !LocalPath(next) || len(next) > maxNextURLLen {
		return ""
	}
	return next
}

// LocalPath reports whether next, a redirect target a request supplied,
// stays on this site: a path starting with "/" that a browser will not read
// as another host. "//evil.example" is protocol-relative, and browsers treat
// "\" as "/", so "/\evil.example" is too; a control character could hide
// either from a check like this one, and browsers strip tabs and newlines
// before resolving.
func LocalPath(next string) bool {
	if len(next) == 0 || next[0] != '/' {
		return false
	}
	if len(next) > 1 && (next[1] == '/' || next[1] == '\\') {
		return false
	}
	for i := 0; i < len(next); i++ {
		if next[i] < 0x20 || next[i] == 0x7f {
			return false
		}
	}
	return true
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
		if _, err := s.db.Exec(`DELETE FROM mfa_pending WHERE token = ?`, s.hash(token)); err != nil {
			slog.Warn("mfa pending delete expired", "err", err)
		}
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
