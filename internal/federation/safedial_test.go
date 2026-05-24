package federation

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// --- addressBlocked ---

func TestAddressBlocked_PrivateRanges(t *testing.T) {
	blocked := []string{
		// IPv4
		"0.0.0.0", "10.1.2.3", "100.64.5.6", "127.0.0.1",
		"169.254.169.254", // AWS/GCP metadata
		"172.16.0.1", "192.0.0.1", "192.168.1.1",
		"198.18.0.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		// IPv6
		"::1",     // loopback
		"fc00::1", // ULA
		"fe80::1", // link-local
		"ff00::1", // multicast
	}
	for _, s := range blocked {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if !addressBlocked(ip) {
			t.Errorf("%s should be blocked", s)
		}
	}
}

func TestAddressBlocked_PublicRangesAllowed(t *testing.T) {
	allowed := []string{
		"1.1.1.1",              // Cloudflare
		"8.8.8.8",              // Google DNS
		"140.82.121.4",         // GitHub
		"2606:4700:4700::1111", // Cloudflare IPv6
	}
	for _, s := range allowed {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if addressBlocked(ip) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestAddressBlocked_IPv4MappedIPv6(t *testing.T) {
	// ::ffff:127.0.0.1 represents 127.0.0.1 as IPv6. The guard must unmap
	// and still block (defense against bypass via IPv4-mapped notation).
	ip, err := netip.ParseAddr("::ffff:127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !addressBlocked(ip) {
		t.Fatal("IPv4-mapped IPv6 loopback should be blocked")
	}
}

func TestAddressBlocked_AllowPrivateOverride(t *testing.T) {
	SetAllowPrivate(true)
	t.Cleanup(func() { SetAllowPrivate(false) })

	ip := netip.MustParseAddr("127.0.0.1")
	if addressBlocked(ip) {
		t.Fatal("SetAllowPrivate(true) should permit loopback")
	}
}

// --- safeDialContext ---

func TestSafeDial_LoopbackRejected(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected dial to 127.0.0.1 to fail")
	}
	if !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("expected ErrBlockedIP, got %v", err)
	}
}

func TestSafeDial_LinkLocalMetadataRejected(t *testing.T) {
	// AWS/GCP metadata IP — should be refused.
	_, err := safeDialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatal("expected dial to metadata IP to fail")
	}
	if !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("expected ErrBlockedIP, got %v", err)
	}
}

func TestSafeDial_AllowPrivate(t *testing.T) {
	// With the override on, dialing localhost should not return ErrBlockedIP.
	SetAllowPrivate(true)
	t.Cleanup(func() { SetAllowPrivate(false) })

	// Start a real listener so dial actually succeeds.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// httptest exposes srv.URL like "http://127.0.0.1:XXXX".
	addr := strings.TrimPrefix(srv.URL, "http://")
	conn, err := safeDialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial with AllowPrivate failed: %v", err)
	}
	conn.Close()
}

func TestSafeDial_BadAddr(t *testing.T) {
	_, err := safeDialContext(context.Background(), "tcp", "no-port-here")
	if err == nil {
		t.Fatal("expected error on bad addr")
	}
}

func TestSafeDial_DNSLookupFails(t *testing.T) {
	// A hostname that cannot resolve. Use a very-likely-invalid TLD.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := safeDialContext(ctx, "tcp", "this-does-not-resolve-xyz123.invalid:80")
	if err == nil {
		t.Fatal("expected lookup failure to surface")
	}
}

// --- normalizeHost ---

func TestNormalizeHost_PrependsHTTPS(t *testing.T) {
	if got := normalizeHost("cog.example.com"); got != "https://cog.example.com" {
		t.Fatalf("got %q, want https://cog.example.com", got)
	}
}

func TestNormalizeHost_HTTPSPassthrough(t *testing.T) {
	if got := normalizeHost("https://cog.example.com/"); got != "https://cog.example.com" {
		t.Fatalf("trailing slash not trimmed: %q", got)
	}
}

func TestNormalizeHost_HTTPUpgradedInProduction(t *testing.T) {
	// In production (allowPrivate=false), http:// → https:// upgrade.
	if got := normalizeHost("http://cog.example.com"); got != "https://cog.example.com" {
		t.Fatalf("got %q, want https-upgraded", got)
	}
}

func TestNormalizeHost_HTTPAllowedWithOverride(t *testing.T) {
	SetAllowPrivate(true)
	t.Cleanup(func() { SetAllowPrivate(false) })

	if got := normalizeHost("http://localhost:8580"); got != "http://localhost:8580" {
		t.Fatalf("got %q, want http preserved", got)
	}
}

// --- noRedirect ---

func TestNoRedirect_AlwaysReturnsUseLastResponse(t *testing.T) {
	err := noRedirect(nil, nil)
	if err != http.ErrUseLastResponse {
		t.Fatalf("got %v, want http.ErrUseLastResponse", err)
	}
}

// --- safeTransport (smoke) ---

func TestSafeTransport_HasDialContextAndTimeouts(t *testing.T) {
	tr := safeTransport()
	if tr.DialContext == nil {
		t.Fatal("DialContext not set")
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Fatal("ResponseHeaderTimeout not set")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Fatal("TLSHandshakeTimeout not set")
	}
}

// --- PeerAllowed ---

func TestPeerAllowed_EmptyAllowsAll(t *testing.T) {
	SetPeerAllowlist("")
	if !PeerAllowed("any.host.example.com") {
		t.Fatal("empty allowlist should allow any peer")
	}
}

func TestPeerAllowed_MatchHost(t *testing.T) {
	SetPeerAllowlist("good.example.com, other.example.com")
	t.Cleanup(func() { SetPeerAllowlist("") })

	if !PeerAllowed("good.example.com") {
		t.Fatal("listed host should be allowed")
	}
	if !PeerAllowed("other.example.com") {
		t.Fatal("second listed host should be allowed")
	}
	if PeerAllowed("evil.example.com") {
		t.Fatal("unlisted host must be rejected")
	}
}

func TestPeerAllowed_StripsScheme(t *testing.T) {
	SetPeerAllowlist("https://cog.example.com")
	t.Cleanup(func() { SetPeerAllowlist("") })

	if !PeerAllowed("cog.example.com") {
		t.Fatal("host without scheme should match scheme-prefixed entry")
	}
	if !PeerAllowed("https://cog.example.com") {
		t.Fatal("host with https scheme should match")
	}
	if !PeerAllowed("http://cog.example.com/") {
		t.Fatal("host with http scheme + trailing slash should match (normalize)")
	}
}

func TestPeerAllowed_CaseInsensitive(t *testing.T) {
	SetPeerAllowlist("COG.EXAMPLE.COM")
	t.Cleanup(func() { SetPeerAllowlist("") })

	if !PeerAllowed("cog.example.com") {
		t.Fatal("matching should be case-insensitive")
	}
}
