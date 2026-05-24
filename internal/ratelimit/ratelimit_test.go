package ratelimit_test

import (
	"net/http"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
)

// trustLocalhost configures the limiter to honor headers from 192.168.1.0/24.
func trustLocalhost() {
	ratelimit.SetTrustedProxies("192.168.1.0/24")
}

func untrustAll() {
	ratelimit.SetTrustedProxies("")
}

func TestClientIP_XRealIP_FromTrustedProxy(t *testing.T) {
	trustLocalhost()
	defer untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{"X-Real-Ip": {"10.0.0.42"}},
	}
	if got := ratelimit.ClientIP(r); got != "10.0.0.42" {
		t.Fatalf("ClientIP = %q, want %q", got, "10.0.0.42")
	}
}

func TestClientIP_XForwardedFor_FromTrustedProxy(t *testing.T) {
	trustLocalhost()
	defer untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{"X-Forwarded-For": {"203.0.113.1, 70.41.3.18"}},
	}
	// Right-to-left walk: 70.41.3.18 is untrusted, so it wins.
	if got := ratelimit.ClientIP(r); got != "70.41.3.18" {
		t.Fatalf("ClientIP = %q, want %q", got, "70.41.3.18")
	}
}

func TestClientIP_XForwardedForTakesPrecedence(t *testing.T) {
	trustLocalhost()
	defer untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header: http.Header{
			"X-Forwarded-For": {"1.2.3.4"},
			"X-Real-Ip":       {"5.6.7.8"},
		},
	}
	// XFF is checked first.
	if got := ratelimit.ClientIP(r); got != "1.2.3.4" {
		t.Fatalf("ClientIP = %q, want %q", got, "1.2.3.4")
	}
}

func TestClientIP_RemoteAddrFallback(t *testing.T) {
	untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{},
	}
	if got := ratelimit.ClientIP(r); got != "192.168.1.1" {
		t.Fatalf("ClientIP = %q, want %q", got, "192.168.1.1")
	}
}

func TestClientIP_UntrustedProxyHeadersIgnored(t *testing.T) {
	// No trusted proxies — XFF/XRI must be ignored.
	untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header: http.Header{
			"X-Forwarded-For": {"1.2.3.4"},
			"X-Real-Ip":       {"5.6.7.8"},
		},
	}
	if got := ratelimit.ClientIP(r); got != "192.168.1.1" {
		t.Fatalf("ClientIP = %q, want %q (XFF must be ignored when peer is untrusted)", got, "192.168.1.1")
	}
}

func TestClientIP_AllTrustedHopsFallthroughToRemote(t *testing.T) {
	ratelimit.SetTrustedProxies("192.168.1.0/24,10.0.0.0/8")
	defer untrustAll()
	r := &http.Request{
		RemoteAddr: "192.168.1.1:12345",
		Header:     http.Header{"X-Forwarded-For": {"10.0.0.5, 10.0.0.6"}},
	}
	// Every hop trusted; fall through to RemoteAddr.
	if got := ratelimit.ClientIP(r); got != "192.168.1.1" {
		t.Fatalf("ClientIP = %q, want %q", got, "192.168.1.1")
	}
}
