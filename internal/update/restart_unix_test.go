//go:build !windows

package update

import "testing"

// TestSupervised: only the marker setup's service sets counts, not the
// variables a terminal under systemd or launchd inherits.
func TestSupervised(t *testing.T) {
	t.Setenv("COG_SUPERVISED", "")
	t.Setenv("INVOCATION_ID", "0123456789abcdef")
	t.Setenv("XPC_SERVICE_NAME", "application.com.microsoft.VSCode.123")
	if Supervised() {
		t.Fatal("a terminal's shell is not supervised")
	}
	t.Setenv("COG_SUPERVISED", "1")
	if !Supervised() {
		t.Fatal("setup's service is supervised")
	}
}
