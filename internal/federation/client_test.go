package federation

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

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
	safehttp.SetAllowPrivate(true)
	t.Cleanup(func() { safehttp.SetAllowPrivate(false) })

	if got := normalizeHost("http://localhost:8580"); got != "http://localhost:8580" {
		t.Fatalf("got %q, want http preserved", got)
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
