package recipes_test

import (
	"os"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
)

// TestMain lets the plugin's client reach the tests' servers, which are
// httptest servers on loopback: outside tests it dials only public addresses.
func TestMain(m *testing.M) {
	safehttp.SetAllowPrivate(true)
	os.Exit(m.Run())
}
