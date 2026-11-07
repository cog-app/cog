// Package ratelimit limits how often a client or an account may fail to
// sign in, and other attempts worth counting.
package ratelimit

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
)

// trustedProxies is the set of prefixes whose forwarding headers are honored.
// Set once at startup via SetTrustedProxies. A request from outside these
// ranges has its X-Forwarded-For / X-Real-IP headers ignored entirely.
var trustedProxies []netip.Prefix

// SetTrustedProxies installs the trusted-proxy prefix list. Pass a
// comma-separated list of CIDR ranges (e.g. "172.16.0.0/12,::1/128").
// Empty values and unparseable entries are silently skipped.
func SetTrustedProxies(spec string) {
	var prefixes []netip.Prefix
	for raw := range strings.SplitSeq(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if p, err := netip.ParsePrefix(raw); err == nil {
			prefixes = append(prefixes, p)
			continue
		}
		// Allow bare IPs as /32 or /128.
		if a, err := netip.ParseAddr(raw); err == nil {
			bits := 32
			if a.Is6() && !a.Is4In6() {
				bits = 128
			}
			prefixes = append(prefixes, netip.PrefixFrom(a, bits))
		}
	}
	trustedProxies = prefixes
}

func isTrusted(ip netip.Addr) bool {
	for _, p := range trustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Limiter tracks failed attempts per key (an address, an account) and
// limits a key that exceeds MaxAttempts within the sliding Window.
//
// A server's limiters keep their attempts in the system database (NewShared),
// so a restart does not forgive a guesser and every replica counts the same
// attempts; New keeps them in memory, for a limiter nothing else shares.
type Limiter struct {
	store store
	clock clock.Clock

	// MaxAttempts is the number of failed attempts allowed in Window.
	MaxAttempts int
	// Window is the sliding time window for counting attempts.
	Window time.Duration
}

// store holds a limiter's attempts.
type store interface {
	record(key string, at, cutoff time.Time) error
	count(key string, cutoff time.Time) (int, error)
	reset(key string) error
	// over returns the keys with at least max attempts since cutoff.
	over(max int, cutoff time.Time) ([]Over, error)
	clear() error
}

// Over is a key a limiter is limiting: Attempts failures within the window,
// the last at Last.
type Over struct {
	Key      string
	Attempts int
	Last     time.Time
}

// New creates a rate limiter that keeps its attempts in memory.
func New(maxAttempts int, window time.Duration, c clock.Clock) *Limiter {
	return &Limiter{store: &memStore{attempts: map[string][]time.Time{}}, clock: c, MaxAttempts: maxAttempts, Window: window}
}

// NewShared creates a rate limiter that keeps its attempts in db's
// rate_attempts table under name, which keeps limiters apart.
func NewShared(db *sql.DB, name string, maxAttempts int, window time.Duration, c clock.Clock) *Limiter {
	return &Limiter{store: &dbStore{db: db, name: name}, clock: c, MaxAttempts: maxAttempts, Window: window}
}

// RecordFailure records a failed attempt for the given key.
func (l *Limiter) RecordFailure(key string) {
	now := l.clock()
	if err := l.store.record(key, now, now.Add(-l.Window)); err != nil {
		slog.Error("rate limit: record attempt", "err", err)
	}
}

// IsLimited reports whether the key has exceeded the rate limit, or an
// error when the attempts could not be counted. A caller refuses what it
// was asked on an error too, but as the server's failure, not the
// client's: told "too many attempts", a user who made none is misled.
func (l *Limiter) IsLimited(key string) (bool, error) {
	n, err := l.store.count(key, l.clock().Add(-l.Window))
	if err != nil {
		return false, fmt.Errorf("rate limit: %w", err)
	}
	return n >= l.MaxAttempts, nil
}

// Reset clears rate limit state for a key (e.g. after successful login).
func (l *Limiter) Reset(key string) {
	if err := l.store.reset(key); err != nil {
		slog.Error("rate limit: reset", "err", err)
	}
}

// Over returns the keys the limiter is limiting now, most recent first.
func (l *Limiter) Over() ([]Over, error) {
	over, err := l.store.over(l.MaxAttempts, l.clock().Add(-l.Window))
	if err != nil {
		return nil, err
	}
	slices.SortFunc(over, func(a, b Over) int { return b.Last.Compare(a.Last) })
	return over, nil
}

// Clear forgets every attempt the limiter holds, so nothing is limited.
func (l *Limiter) Clear() error {
	return l.store.clear()
}

// memStore keeps attempts in a map. Each record drops the attempts that
// have left the window, so keys no one tries again do not pile up.
type memStore struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func (m *memStore) record(key string, at, cutoff time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, a := range m.attempts {
		a = slices.DeleteFunc(a, func(t time.Time) bool { return t.Before(cutoff) })
		if len(a) == 0 {
			delete(m.attempts, k)
		} else {
			m.attempts[k] = a
		}
	}
	m.attempts[key] = append(m.attempts[key], at)
	return nil
}

func (m *memStore) count(key string, cutoff time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, t := range m.attempts[key] {
		if !t.Before(cutoff) {
			n++
		}
	}
	return n, nil
}

func (m *memStore) reset(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.attempts, key)
	return nil
}

func (m *memStore) over(max int, cutoff time.Time) ([]Over, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var over []Over
	for k, a := range m.attempts {
		o := Over{Key: k}
		for _, t := range a {
			if !t.Before(cutoff) {
				o.Attempts++
				if t.After(o.Last) {
					o.Last = t
				}
			}
		}
		if o.Attempts >= max {
			over = append(over, o)
		}
	}
	return over, nil
}

func (m *memStore) clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.attempts)
	return nil
}

// dbStore keeps attempts in the rate_attempts table. Recording drops the
// limiter's attempts that have left the window: only failures are recorded,
// so the table stays small.
type dbStore struct {
	db   *sql.DB
	name string
}

func (d *dbStore) record(key string, at, cutoff time.Time) error {
	if _, err := d.db.Exec(`DELETE FROM rate_attempts WHERE limiter = ? AND at < ?`, d.name, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("prune %s: %w", d.name, err)
	}
	if _, err := d.db.Exec(`INSERT INTO rate_attempts (limiter, key, at) VALUES (?, ?, ?)`, d.name, key, at.UnixMilli()); err != nil {
		return fmt.Errorf("record %s: %w", d.name, err)
	}
	return nil
}

func (d *dbStore) count(key string, cutoff time.Time) (int, error) {
	var n int
	if err := d.db.QueryRow(`SELECT count(*) FROM rate_attempts WHERE limiter = ? AND key = ? AND at >= ?`,
		d.name, key, cutoff.UnixMilli()).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", d.name, err)
	}
	return n, nil
}

func (d *dbStore) reset(key string) error {
	if _, err := d.db.Exec(`DELETE FROM rate_attempts WHERE limiter = ? AND key = ?`, d.name, key); err != nil {
		return fmt.Errorf("reset %s: %w", d.name, err)
	}
	return nil
}

func (d *dbStore) over(max int, cutoff time.Time) ([]Over, error) {
	rows, err := d.db.Query(`SELECT key, count(*), max(at) FROM rate_attempts WHERE limiter = ? AND at >= ? GROUP BY key HAVING count(*) >= ?`,
		d.name, cutoff.UnixMilli(), max)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", d.name, err)
	}
	defer rows.Close()
	var over []Over
	for rows.Next() {
		var (
			o    Over
			last int64
		)
		if err := rows.Scan(&o.Key, &o.Attempts, &last); err != nil {
			return nil, fmt.Errorf("list %s: %w", d.name, err)
		}
		o.Last = time.UnixMilli(last)
		over = append(over, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", d.name, err)
	}
	return over, nil
}

func (d *dbStore) clear() error {
	if _, err := d.db.Exec(`DELETE FROM rate_attempts WHERE limiter = ?`, d.name); err != nil {
		return fmt.Errorf("clear %s: %w", d.name, err)
	}
	return nil
}

// ClientIP extracts the client IP from a request.
//
// If the immediate connection (r.RemoteAddr) is in the trusted-proxy list,
// X-Forwarded-For is walked right-to-left, skipping further trusted hops, and
// the first untrusted address is returned. X-Real-IP is honored only when XFF
// is absent. If the connection is NOT from a trusted proxy, the forwarding
// headers are ignored entirely and RemoteAddr is returned — this prevents any
// client from spoofing its rate-limit identity.
func ClientIP(r *http.Request) string {
	remote := remoteAddrIP(r.RemoteAddr)
	remoteAddr, _ := netip.ParseAddr(remote)

	if !remoteAddr.IsValid() || !isTrusted(remoteAddr) {
		return remote
	}

	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for _, hop := range slices.Backward(hops) {
			hop := strings.TrimSpace(hop)
			if hop == "" {
				continue
			}
			addr, err := netip.ParseAddr(hop)
			if err != nil {
				return hop // best-effort
			}
			if !isTrusted(addr) {
				return hop
			}
		}
		// All hops trusted; fall through to RemoteAddr.
	}

	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	return remote
}

// remoteAddrIP strips the port suffix from an "ip:port" or "[ipv6]:port" string.
func remoteAddrIP(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}
