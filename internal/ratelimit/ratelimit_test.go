package ratelimit_test

import (
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
)

func TestClientIP_XRealIP(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{"X-Real-Ip": {"10.0.0.42"}},
	}
	if got := ratelimit.ClientIP(r); got != "10.0.0.42" {
		t.Fatalf("ClientIP = %q, want %q", got, "10.0.0.42")
	}
}

func TestClientIP_XForwardedFor(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.1, 70.41.3.18"}},
	}
	if got := ratelimit.ClientIP(r); got != "203.0.113.1" {
		t.Fatalf("ClientIP = %q, want %q", got, "203.0.113.1")
	}
}

func TestClientIP_XForwardedForTakesPrecedence(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header: http.Header{
			"X-Forwarded-For": {"1.2.3.4"},
			"X-Real-Ip":       {"5.6.7.8"},
		},
	}
	// X-Forwarded-For is checked first.
	if got := ratelimit.ClientIP(r); got != "1.2.3.4" {
		t.Fatalf("ClientIP = %q, want %q", got, "1.2.3.4")
	}
}

func TestClientIP_RemoteAddrFallback(t *testing.T) {
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{},
	}
	if got := ratelimit.ClientIP(r); got != "192.168.1.1" {
		t.Fatalf("ClientIP = %q, want %q", got, "192.168.1.1")
	}
}
