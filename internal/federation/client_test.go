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
