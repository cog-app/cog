// Package ratelimit provides a simple in-memory rate limiter for auth endpoints.
package ratelimit

import (
	"net/http"
	"sync"
	"time"
)

// Limiter tracks failed authentication attempts per key (IP or username)
// and blocks requests that exceed the threshold within a sliding window.
type Limiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time

	// MaxAttempts is the number of failed attempts allowed in Window.
	MaxAttempts int
	// Window is the sliding time window for counting attempts.
	Window time.Duration
}

// New creates a rate limiter with the given threshold and window.
func New(maxAttempts int, window time.Duration) *Limiter {
	l := &Limiter{
		attempts:    make(map[string][]time.Time),
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
	l.attempts[key] = append(l.attempts[key], time.Now())
}

// IsLimited returns true if the key has exceeded the rate limit.
func (l *Limiter) IsLimited(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-l.Window)
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
		cutoff := time.Now().Add(-l.Window)
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

// ClientIP extracts the client IP from a request, checking X-Forwarded-For first.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Use the leftmost IP (original client).
		for i := 0; i < len(xff); i++ {
			if xff[i] == ',' {
				return xff[:i]
			}
		}
		return xff
	}
	if xff := r.Header.Get("X-Real-Ip"); xff != "" {
		return xff
	}
	// Strip port from RemoteAddr.
	addr := r.RemoteAddr
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
