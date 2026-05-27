package web_test

import (
	"net/http"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/ui"
)

// TestStaticAssetsVersionedAndImmutable verifies the core static handler is
// wired to ui.Static: the login page references a content-hashed CSS URL, that
// URL is served with an immutable Cache-Control, and an unversioned request
// falls back to no-cache.
func TestStaticAssetsVersionedAndImmutable(t *testing.T) {
	env := cogtest.New(t)

	// The unauthenticated login page must reference the versioned stylesheet.
	body := env.Get(t, "/login").Status(http.StatusOK).ReadBody()
	if !strings.Contains(body, "/static/style.css?v=") {
		t.Errorf("login page missing versioned style.css URL; body len=%d", len(body))
	}

	// The versioned URL is served immutable.
	versioned := ui.Static.URL("style.css") // /static/style.css?v=<hash>
	resp := env.Get(t, versioned).Status(http.StatusOK)
	assert.Equal(t, "public, max-age=31536000, immutable", resp.Header.Get("Cache-Control"))
	if resp.Header.Get("ETag") == "" {
		t.Error("versioned asset missing ETag")
	}

	// An unversioned request still serves but must revalidate.
	resp = env.Get(t, "/static/style.css").Status(http.StatusOK)
	assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
}
