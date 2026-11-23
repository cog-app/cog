package web_test

import (
	"crypto/ed25519"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/update"
)

// TestAdminUpdatesWithoutUpdater: a build without the updater (nosetup)
// says who updates it.
func TestAdminUpdatesWithoutUpdater(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, _ := sessionAndCSRF(t, env, admin.ID)
	body := env.Get(t, "/admin/updates", cookie).Status(http.StatusOK).ReadBody()
	require.Contains(t, body, "updated by whoever packaged it")
}

// TestAdminUpdatesCheck: Check now reads the signed release list and the
// page offers the newer release.
func TestAdminUpdatesCheck(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")
	cookie, token := sessionAndCSRF(t, env, admin.ID)

	pub, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	data, err := json.Marshal(update.Manifest{Releases: []update.Release{{
		Version: "v9.0.0", NotesURL: "https://example.test/notes",
		Assets: map[string]update.Asset{"test-platform": {URL: "https://example.test/cog", SHA256: "00", Size: 1}},
	}}})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/releases.json":
			w.Write(data)
		case "/releases.json.sig":
			w.Write(update.Sign(priv, data))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	env.Providers.Updates = &update.Checker{
		URL: srv.URL + "/releases.json", Current: "v1.0.0", Platform: "test-platform",
		Exe: t.TempDir() + "/cog", Keys: []ed25519.PublicKey{pub}, Client: srv.Client(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Restart: func() {},
	}

	env.PostForm(t, "/admin/updates/check", url.Values{"csrf_token": {token}}, cookie, cogtest.StopRedirect).
		Status(http.StatusSeeOther)
	body := env.Get(t, "/admin/updates", cookie).Status(http.StatusOK).ReadBody()
	require.Contains(t, body, "Cog v9.0.0 is available.")
	require.Contains(t, body, "Install and restart")
	require.Contains(t, body, "https://example.test/notes")
}
