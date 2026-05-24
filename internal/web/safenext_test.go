package web

import "testing"

// TestSafeNext_RedirectFilter pins the rules safeNext enforces so future
// changes don't accidentally widen the open-redirect attack surface
// referenced in audit security.md #18.
func TestSafeNext_RedirectFilter(t *testing.T) {
	cases := []struct {
		in, want string
		why      string
	}{
		// Allowed.
		{"/", "/", "root path"},
		{"/settings/security", "/settings/security", "in-app absolute path"},
		{"/files?q=1", "/files?q=1", "path with query"},

		// Rejected — fall back to "/".
		{"", "/", "empty"},
		{"//evil.com/phish", "/", "protocol-relative URL"},
		{"http://evil.com", "/", "explicit URL"},
		{"https://evil.com", "/", "explicit URL"},
		{"javascript:alert(1)", "/", "javascript scheme"},
		{"relative/path", "/", "missing leading slash"},
		{"./relative", "/", "relative path"},
	}
	for _, c := range cases {
		got := safeNext(c.in)
		if got != c.want {
			t.Errorf("safeNext(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}

// TestLoginRedirect pins URL construction.
func TestLoginRedirect_BuildsNextQuery(t *testing.T) {
	if got := loginRedirect("/login", ""); got != "/login" {
		t.Fatalf("empty next: got %q, want /login", got)
	}
	if got := loginRedirect("/login", "/files"); got != "/login?next=%2Ffiles" {
		t.Fatalf("plain next: got %q", got)
	}
	if got := loginRedirect("/login", "/with spaces"); got != "/login?next=%2Fwith+spaces" {
		t.Fatalf("urlencoded: got %q", got)
	}
}
