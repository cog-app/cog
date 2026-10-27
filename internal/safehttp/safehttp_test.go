package safehttp

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
		if !addressBlocked(ip, nil) {
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
		if addressBlocked(ip, nil) {
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
	if !addressBlocked(ip, nil) {
		t.Fatal("IPv4-mapped IPv6 loopback should be blocked")
	}
}

func TestAddressBlocked_AllowPrivateOverride(t *testing.T) {
	SetAllowPrivate(true)
	t.Cleanup(func() { SetAllowPrivate(false) })

	ip := netip.MustParseAddr("127.0.0.1")
	if addressBlocked(ip, nil) {
		t.Fatal("SetAllowPrivate(true) should permit loopback")
	}
}

// --- DialContext ---

func TestSafeDial_LoopbackRejected(t *testing.T) {
	_, err := DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected dial to 127.0.0.1 to fail")
	}
	if !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("expected ErrBlockedIP, got %v", err)
	}
}

func TestSafeDial_LinkLocalMetadataRejected(t *testing.T) {
	// AWS/GCP metadata IP — should be refused.
	_, err := DialContext(context.Background(), "tcp", "169.254.169.254:80")
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
	conn, err := DialContext(context.Background(), "tcp", addr)
	if err != nil {
		t.Fatalf("dial with AllowPrivate failed: %v", err)
	}
	conn.Close()
}

func TestSafeDial_BadAddr(t *testing.T) {
	_, err := DialContext(context.Background(), "tcp", "no-port-here")
	if err == nil {
		t.Fatal("expected error on bad addr")
	}
}

func TestSafeDial_DNSLookupFails(t *testing.T) {
	// A hostname that cannot resolve. Use a very-likely-invalid TLD.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := DialContext(ctx, "tcp", "this-does-not-resolve-xyz123.invalid:80")
	if err == nil {
		t.Fatal("expected lookup failure to surface")
	}
}

// --- normalizeHost ---

// --- NoRedirect ---

func TestNoRedirect_AlwaysReturnsUseLastResponse(t *testing.T) {
	err := NoRedirect(nil, nil)
	if err != http.ErrUseLastResponse {
		t.Fatalf("got %v, want http.ErrUseLastResponse", err)
	}
}

// --- Transport (smoke) ---

func TestSafeTransport_HasDialContextAndTimeouts(t *testing.T) {
	tr := Transport()
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

// TestClientWithAllow reaches a loopback server through one client while
// every other client is still refused.
func TestClientWithAllow(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	tlsConf := srv.Client().Transport.(*http.Transport).TLSClientConfig

	allowed := ClientWith(5*time.Second, Options{Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}, TLS: tlsConf})
	resp, err := allowed.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowed client: %v", err)
	}
	resp.Body.Close()

	if _, err := Client(5 * time.Second).Get(srv.URL); !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("plain client: expected ErrBlockedIP, got %v", err)
	}
	other := ClientWith(5*time.Second, Options{Allow: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}, TLS: tlsConf})
	if _, err := other.Get(srv.URL); !errors.Is(err, ErrBlockedIP) {
		t.Fatalf("client allowed elsewhere: expected ErrBlockedIP, got %v", err)
	}
}
