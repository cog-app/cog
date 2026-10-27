// Package push keeps clients' push subscriptions and delivers to them over
// Web Push. It knows nothing of what is watched: a protocol (WebDAV-Push,
// later JMAP) names the resource, marks subscriptions when it changes, and
// writes the message.
//
// Subscriptions live in the system database, so every replica sees the same
// ones and any of them can deliver. A change is recorded on the
// subscription as owed (pending) rather than queued per change, so any
// number of changes before a delivery make one push.
package push

import (
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/webpush"
)

// Protocol names who made a subscription and decides what its resource
// and options mean.
type Protocol string

const (
	WebDAV Protocol = "webdav" // draft-bitfire-webdav-push
	JMAP   Protocol = "jmap"   // RFC 8620 section 7.2
)

// Subscription is a push endpoint and what it watches.
type Subscription struct {
	ID       string // random; names the registration to the client
	UserID   string // who registered it
	Protocol Protocol
	Resource string // what it watches, in the protocol's terms
	Endpoint string // the push resource (RFC 8030)
	Keys     webpush.Keys
	Options  string // the protocol's own settings, such as triggers
	Expires  time.Time

	// Pending is the protocol's bitmask of changes owed to the client;
	// zero when nothing is.
	Pending uint
	// seq counts the changes marked, so a delivery clears only what it
	// sent and not a change marked while it was sending.
	seq int64

	Failures      int
	LastError     string
	LastSuccessAt time.Time
	NextAttemptAt time.Time
	CreatedAt     time.Time
}

// ErrNotFound reports a subscription that does not exist, has expired, or
// belongs to someone else.
var ErrNotFound = errors.New("push: no such subscription")

// ErrTaken reports a registration of an endpoint another user registered
// for the same resource.
var ErrTaken = errors.New("push: endpoint registered by another user")

// Store keeps subscriptions in the system database.
type Store struct {
	db    *sql.DB
	clock clock.Clock
}

// NewStore returns a store over the system database.
func NewStore(db *sql.DB, c clock.Clock) *Store { return &Store{db: db, clock: c} }

const columns = `id, user_id, protocol, resource, endpoint, p256dh, auth, options, expires_at,
	pending, seq, failures, last_error, last_success_at, next_attempt_at, created_at`

func scan(row interface{ Scan(...any) error }) (*Subscription, error) {
	var s Subscription
	var expires, success, next, created int64
	if err := row.Scan(&s.ID, &s.UserID, &s.Protocol, &s.Resource, &s.Endpoint, &s.Keys.P256DH, &s.Keys.Auth,
		&s.Options, &expires, &s.Pending, &s.seq, &s.Failures, &s.LastError, &success, &next, &created); err != nil {
		return nil, err
	}
	s.Expires = time.Unix(expires, 0).UTC()
	if success > 0 {
		s.LastSuccessAt = time.Unix(success, 0).UTC()
	}
	if next > 0 {
		s.NextAttemptAt = time.Unix(next, 0).UTC()
	}
	s.CreatedAt = time.Unix(created, 0).UTC()
	return &s, nil
}

func (st *Store) query(q string, args ...any) ([]Subscription, error) {
	rows, err := st.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("push subscriptions: %w", err)
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan push subscription: %w", err)
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// newID names a registration: 128 random bits, safe in a URL.
func newID() string { return strings.ToLower(rand.Text()) }

// Register stores s, or, when its user already registered the endpoint for
// the resource, updates that registration's keys, options and expiry (the
// endpoint identifies a subscription). It fills s.ID and reports whether
// the subscription is new.
func (st *Store) Register(s *Subscription) (created bool, err error) {
	if err := s.Keys.Validate(); err != nil {
		return false, err
	}
	now := st.clock().Unix()
	id := newID()
	var got string
	err = st.db.QueryRow(`INSERT INTO push_subscriptions (id, user_id, protocol, resource, endpoint, p256dh, auth, options,
			expires_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(protocol, resource, endpoint) DO UPDATE SET
			p256dh = excluded.p256dh, auth = excluded.auth, options = excluded.options,
			expires_at = excluded.expires_at, updated_at = excluded.updated_at
		WHERE user_id = excluded.user_id
		RETURNING id`,
		id, s.UserID, s.Protocol, s.Resource, s.Endpoint, s.Keys.P256DH, s.Keys.Auth, s.Options,
		s.Expires.Unix(), now, now).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrTaken
	}
	if err != nil {
		return false, fmt.Errorf("register push subscription: %w", err)
	}
	s.ID = got
	return got == id, nil
}

// Get returns a subscription that has not expired.
func (st *Store) Get(id string) (*Subscription, error) {
	s, err := scan(st.db.QueryRow(`SELECT `+columns+` FROM push_subscriptions WHERE id = ? AND expires_at > ?`, id, st.clock().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("push subscription %s: %w", id, err)
	}
	return s, nil
}

// Unregister removes a user's subscription. Someone else's, or an expired
// one, is ErrNotFound.
func (st *Store) Unregister(id, userID string) error {
	res, err := st.db.Exec(`DELETE FROM push_subscriptions WHERE id = ? AND user_id = ? AND expires_at > ?`, id, userID, st.clock().Unix())
	if err != nil {
		return fmt.Errorf("unregister push subscription %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("unregister push subscription %s: %w", id, err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Remove deletes subscriptions, whoever owns them: ones the push service
// no longer knows, ones whose user lost access to what they watch.
func (st *Store) Remove(ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := st.db.Exec(`DELETE FROM push_subscriptions WHERE id IN (`+placeholders(len(ids))+`)`, anys(ids)...); err != nil {
		return fmt.Errorf("remove push subscriptions: %w", err)
	}
	return nil
}

// For returns the unexpired subscriptions of a protocol to any of
// resources.
func (st *Store) For(p Protocol, resources []string) ([]Subscription, error) {
	if len(resources) == 0 {
		return nil, nil
	}
	args := append([]any{p, st.clock().Unix()}, anys(resources)...)
	return st.query(`SELECT `+columns+` FROM push_subscriptions WHERE protocol = ? AND expires_at > ? AND resource IN (`+
		placeholders(len(resources))+`)`, args...)
}

// OfUser returns a user's unexpired subscriptions of a protocol.
func (st *Store) OfUser(p Protocol, userID string) ([]Subscription, error) {
	return st.query(`SELECT `+columns+` FROM push_subscriptions WHERE protocol = ? AND user_id = ? AND expires_at > ? ORDER BY created_at, id`,
		p, userID, st.clock().Unix())
}

// Mark records changes (the protocol's bits) as owed to subscriptions. One
// that owed nothing becomes due now; one already owing keeps its schedule,
// so a subscription backing off is not hammered and a burst of changes
// makes one push.
func (st *Store) Mark(bits uint, ids ...string) error {
	if len(ids) == 0 || bits == 0 {
		return nil
	}
	args := append([]any{bits, st.clock().Unix()}, anys(ids)...)
	if _, err := st.db.Exec(`UPDATE push_subscriptions SET pending = pending | ?1, seq = seq + 1,
			next_attempt_at = CASE WHEN pending = 0 THEN ?2 ELSE next_attempt_at END
		WHERE id IN (`+placeholdersFrom(3, len(ids))+`)`, args...); err != nil {
		return fmt.Errorf("mark push subscriptions: %w", err)
	}
	return nil
}

// claim takes up to limit due subscriptions for delivery, leasing them so
// that another replica does not deliver them at the same time.
func (st *Store) claim(lease time.Duration, limit int) ([]Subscription, error) {
	now := st.clock().Unix()
	// Look before claiming: most rounds find nothing due, and the UPDATE
	// would take the system database's write lock to learn that.
	var due bool
	if err := st.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM push_subscriptions WHERE pending != 0 AND next_attempt_at <= ?)`, now).Scan(&due); err != nil {
		return nil, fmt.Errorf("push subscriptions due: %w", err)
	}
	if !due {
		return nil, nil
	}
	return st.query(`UPDATE push_subscriptions SET next_attempt_at = ?
		WHERE id IN (SELECT id FROM push_subscriptions WHERE pending != 0 AND next_attempt_at <= ? AND expires_at > ?
			ORDER BY next_attempt_at LIMIT ?)
		RETURNING `+columns, now+int64(lease/time.Second), now, now, limit)
}

// delivered records a successful push. What was marked while it was being
// sent stays owed and is due at once.
func (st *Store) delivered(s *Subscription) error {
	now := st.clock().Unix()
	if _, err := st.db.Exec(`UPDATE push_subscriptions SET
			pending = CASE WHEN seq = ? THEN 0 ELSE pending END,
			next_attempt_at = CASE WHEN seq = ? THEN 0 ELSE ? END,
			failures = 0, last_error = '', last_success_at = ?
		WHERE id = ?`, s.seq, s.seq, now, now, s.ID); err != nil {
		return fmt.Errorf("record push to %s: %w", s.ID, err)
	}
	return nil
}

// failed records a failed push, due again at next.
func (st *Store) failed(s *Subscription, reason string, next time.Time) error {
	if _, err := st.db.Exec(`UPDATE push_subscriptions SET failures = failures + 1, last_error = ?, next_attempt_at = ? WHERE id = ?`,
		reason, next.Unix(), s.ID); err != nil {
		return fmt.Errorf("record failed push to %s: %w", s.ID, err)
	}
	return nil
}

// Prune deletes expired subscriptions and returns how many.
func (st *Store) Prune() (int64, error) {
	res, err := st.db.Exec(`DELETE FROM push_subscriptions WHERE expires_at <= ?`, st.clock().Unix())
	if err != nil {
		return 0, fmt.Errorf("prune push subscriptions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune push subscriptions: %w", err)
	}
	return n, nil
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// placeholdersFrom numbers n placeholders from first, for statements that
// also use numbered ones.
func placeholdersFrom(first, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "?%d", first+i)
	}
	return b.String()
}

func anys[T any](xs []T) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// ForPrefix returns the unexpired subscriptions of a protocol to resources
// that start with prefix, for a change that reaches resources the caller
// cannot list.
func (st *Store) ForPrefix(p Protocol, prefix string) ([]Subscription, error) {
	return st.query(`SELECT `+columns+` FROM push_subscriptions WHERE protocol = ? AND expires_at > ? AND substr(resource, 1, ?) = ?`,
		p, st.clock().Unix(), len(prefix), prefix)
}
