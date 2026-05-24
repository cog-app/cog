package auth

import "testing"

// TestSanitizeNextURL_RejectsExternal pins the rules the MFA-pending store
// applies at Create time (audit security.md #18). Anything not a same-origin
// /path is dropped.
func TestSanitizeNextURL_RejectsExternal(t *testing.T) {
	cases := []struct {
		in, want, why string
	}{
		{"/files", "/files", "in-app absolute path kept"},
		{"/settings?tab=1", "/settings?tab=1", "path with query kept"},

		{"", "", "empty dropped"},
		{"//evil.com/phish", "", "protocol-relative dropped"},
		{"http://evil.com", "", "http URL dropped"},
		{"https://evil.com", "", "https URL dropped"},
		{"javascript:alert(1)", "", "javascript scheme dropped"},
		{"relative", "", "no-leading-slash dropped"},
		{"./relative", "", "relative path dropped"},
	}
	for _, c := range cases {
		got := sanitizeNextURL(c.in)
		if got != c.want {
			t.Errorf("sanitizeNextURL(%q) = %q, want %q (%s)", c.in, got, c.want, c.why)
		}
	}
}
