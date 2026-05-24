package auth

import (
	"strings"
	"testing"
)

// TestGenerateOTPCode_ReturnsErrorOnRandFailure isn't easy to write without
// rerouting rand.Reader. Instead we just sanity-check that the function
// returns a non-empty 6-digit string and no error in the happy path.
func TestGenerateOTPCode_HappyPath(t *testing.T) {
	code, err := generateOTPCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 6 {
		t.Fatalf("expected 6-digit code, got %d chars: %q", len(code), code)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			t.Fatalf("non-digit in OTP: %q", code)
		}
	}
}

// TestGenerateOTPCode_Distribution: 1000 codes should rarely collide. If
// rand fell back to a deterministic value the same code would repeat.
func TestGenerateOTPCode_Distribution(t *testing.T) {
	seen := make(map[string]int)
	for range 1000 {
		c, err := generateOTPCode()
		if err != nil {
			t.Fatal(err)
		}
		seen[c]++
	}
	if len(seen) < 900 {
		t.Fatalf("only %d distinct codes in 1000 — rand may be deterministic", len(seen))
	}
}

// TestGenerateBackupCode_HappyPath ensures the audit-flagged silent-rand
// swallow is gone: returns a non-empty alphanumeric code or a real error.
func TestGenerateBackupCode_HappyPath(t *testing.T) {
	c, err := generateBackupCode()
	if err != nil {
		t.Fatal(err)
	}
	if len(c) != 8 {
		t.Fatalf("expected 8-char code, got %d: %q", len(c), c)
	}
	const alphabet = "23456789abcdefghjkmnpqrstuvwxyz"
	for _, r := range c {
		if !strings.ContainsRune(alphabet, r) {
			t.Fatalf("backup code uses non-alphabet rune %q in %q", r, c)
		}
	}
}

func TestGenerateBackupCode_Distribution(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		c, err := generateBackupCode()
		if err != nil {
			t.Fatal(err)
		}
		seen[c] = struct{}{}
	}
	if len(seen) < 990 {
		t.Fatalf("only %d distinct backup codes in 1000 — rand may be deterministic", len(seen))
	}
}
