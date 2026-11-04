package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

const SessionCookieName = "cog_session"

// sessionDuration is how long a session lasts without being used. Every use
// pushes the expiry this far out again (at most once per
// sessionRenewInterval), so a session in regular use never expires.
const sessionDuration = 30 * 24 * time.Hour

// sessionRenewInterval limits how often a session's expiry and last-seen time
// are written. A page load and its HTMX follow-ups would otherwise each
// write the same row.
const sessionRenewInterval = time.Hour

// maxUserAgent caps the stored User-Agent; it is display-only.
const maxUserAgent = 512

// ErrSessionNotFound is returned when a session to revoke does not exist or
// belongs to someone else.
var ErrSessionNotFound = errors.New("session not found")

type Session struct {
	ID         string
	UserID     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	IP         string
}

// Handle identifies the session on pages and in URLs. The ID is the bearer
// token in the HttpOnly cookie, so it must never be written into a page.
func (s *Session) Handle() string {
	sum := sha256.Sum256([]byte(s.ID))
	return hex.EncodeToString(sum[:8])
}

// SetCookie writes the session cookie. It lasts as long as the session, so
// the browser keeps it across restarts instead of dropping it on close. It is
// only written when a session starts or is renewed, so that is always a full
// sessionDuration.
//
// Max-Age rather than Expires: an absolute date is judged by the browser's
// clock, and a server clock that is behind would hand out cookies the browser
// considers already expired.
func (s *Session) SetCookie(w http.ResponseWriter, c cogplugin.CookieConfig) {
	c.Set(w, &http.Cookie{
		Name:   SessionCookieName,
		Value:  s.ID,
		MaxAge: int(sessionDuration / time.Second),
	})
}

type SessionStore struct {
	db    *sql.DB
	clock clock.Clock
}

func NewSessionStore(db *sql.DB, c clock.Clock) *SessionStore {
	return &SessionStore{db: db, clock: c}
}

// Create starts a session with no client details. Use Start for a session a
// browser will carry.
func (s *SessionStore) Create(userID string) (*Session, error) {
	return s.create(userID, "", "")
}

// Start starts a session for the client that sent r, recording its
// User-Agent and address so the user can recognise it in Settings.
func (s *SessionStore) Start(userID string, r *http.Request) (*Session, error) {
	ua := r.UserAgent()
	if len(ua) > maxUserAgent {
		ua = ua[:maxUserAgent]
	}
	return s.create(userID, ua, ratelimit.ClientIP(r))
}

func (s *SessionStore) create(userID, userAgent, ip string) (*Session, error) {
	token, err := generateToken(32)
	if err != nil {
		return nil, err
	}

	now := s.clock()
	expires := now.Add(sessionDuration)

	// Expired rows are never read again; dropping the user's here keeps the
	// table from growing without needing a sweeper.
	if _, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND expires_at <= ?`, userID, now.Unix()); err != nil {
		return nil, fmt.Errorf("prune sessions: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, user_agent, ip) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		token, userID, now.Unix(), expires.Unix(), now.Unix(), userAgent, ip,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	return &Session{
		ID:         token,
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  expires,
		LastSeenAt: now,
		UserAgent:  userAgent,
		IP:         ip,
	}, nil
}

const sessionColumns = `id, user_id, created_at, expires_at, last_seen_at, user_agent, ip`

func scanSession(row interface{ Scan(...any) error }) (*Session, error) {
	var sess Session
	var createdAt, expiresAt, lastSeenAt int64
	if err := row.Scan(&sess.ID, &sess.UserID, &createdAt, &expiresAt, &lastSeenAt, &sess.UserAgent, &sess.IP); err != nil {
		return nil, err
	}
	sess.CreatedAt = time.Unix(createdAt, 0)
	sess.ExpiresAt = time.Unix(expiresAt, 0)
	// Sessions from before last_seen_at was recorded hold 0.
	sess.LastSeenAt = sess.CreatedAt
	if lastSeenAt != 0 {
		sess.LastSeenAt = time.Unix(lastSeenAt, 0)
	}
	return &sess, nil
}

func (s *SessionStore) Validate(token string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ? AND expires_at > ?`,
		token, s.clock().Unix(),
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("validate session: %w", err)
	}
	return sess, nil
}

// Renew records that sess was just used and pushes its expiry a full
// sessionDuration out, unless that was already done within
// sessionRenewInterval. It reports whether sess was renewed, in which case
// the caller should reissue the cookie with the new expiry.
func (s *SessionStore) Renew(sess *Session) (bool, error) {
	now := s.clock()
	if now.Sub(sess.LastSeenAt) < sessionRenewInterval {
		return false, nil
	}
	expires := now.Add(sessionDuration)
	res, err := s.db.Exec(
		`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ? AND last_seen_at < ?`,
		now.Unix(), expires.Unix(), sess.ID, now.Add(-sessionRenewInterval).Unix(),
	)
	if err != nil {
		return false, fmt.Errorf("renew session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew session: %w", err)
	}
	if n == 0 {
		// A concurrent request renewed it first.
		return false, nil
	}
	sess.LastSeenAt = now
	sess.ExpiresAt = expires
	return true, nil
}

// ListByUser returns the user's live sessions, most recently used first.
func (s *SessionStore) ListByUser(userID string) ([]Session, error) {
	rows, err := s.db.Query(
		`SELECT `+sessionColumns+` FROM sessions WHERE user_id = ? AND expires_at > ?
		ORDER BY CASE last_seen_at WHEN 0 THEN created_at ELSE last_seen_at END DESC`,
		userID, s.clock().Unix(),
	)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		out = append(out, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return out, nil
}

// DeleteByHandle revokes the user's session whose Handle is handle.
func (s *SessionStore) DeleteByHandle(userID, handle string) error {
	sessions, err := s.ListByUser(userID)
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		if sess.Handle() == handle {
			return s.Delete(sess.ID)
		}
	}
	return ErrSessionNotFound
}

func (s *SessionStore) Delete(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, token)
	return err
}

// DeleteAllExcept removes all sessions for a user except the given session.
func (s *SessionStore) DeleteAllExcept(userID, exceptSessionID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, exceptSessionID)
	return err
}

// DeleteAllForUser removes all sessions for a user.
func (s *SessionStore) DeleteAllForUser(userID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

func generateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
