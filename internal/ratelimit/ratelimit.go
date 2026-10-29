// Package ratelimit provides a simple in-memory rate limiter for auth endpoints.
package ratelimit

import (
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

// Limiter tracks failed authentication attempts per key (IP or username)
// and blocks requests that exceed the threshold within a sliding window.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	clock    clock.Clock

	// MaxAttempts is the number of failed attempts allowed in Window.
	MaxAttempts int
	// Window is the sliding time window for counting attempts.
	Window time.Duration
}

// New creates a rate limiter with the given threshold and window.
func New(maxAttempts int, window time.Duration, c clock.Clock) *Limiter {
	l := &Limiter{
		attempts:    make(map[string][]time.Time),
		clock:       c,
		MaxAttempts: maxAttempts,
		Window:      window,
	}
	go l.cleanup()
	return l
}

// RecordFailure records a failed attempt for the given key.
func (l *Limiter) RecordFailure(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.attempts[key], l.clock())
}

// IsLimited returns true if the key has exceeded the rate limit.
func (l *Limiter) IsLimited(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := l.clock().Add(-l.Window)
	attempts := l.attempts[key]

	// Trim old entries.
	start := 0
	for start < len(attempts) && attempts[start].Before(cutoff) {
		start++
	}
	if start > 0 {
		l.attempts[key] = attempts[start:]
		attempts = l.attempts[key]
	}

	return len(attempts) >= l.MaxAttempts
}

// Reset clears rate limit state for a key (e.g. after successful login).
func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// cleanup periodically removes expired entries.
func (l *Limiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		cutoff := l.clock().Add(-l.Window)
		for key, attempts := range l.attempts {
			start := 0
			for start < len(attempts) && attempts[start].Before(cutoff) {
				start++
			}
			if start >= len(attempts) {
				delete(l.attempts, key)
			} else if start > 0 {
				l.attempts[key] = attempts[start:]
			}
		}
		l.mu.Unlock()
	}
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
