package plugin_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/testplugins"
)

func TestGRPCBridge_RouteServes(t *testing.T) {
	binPath := testplugins.Build(t, "grpcping")

	// LoadExternal mounts routes on the mux web.New already has, so loading
	// after cogtest.New still serves them.
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	resp := env.Get(t, "/apps/grpcping/ping", alice.Auth)
	resp.Status(200).Equals("pong")
}

func TestGRPCBridge_UnauthRoute(t *testing.T) {
	binPath := testplugins.Build(t, "grpcping")
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	// RegisterAPI wraps with RequireAuth — no auth → 401.
	env.Get(t, "/apps/grpcping/ping").Status(401)
}

// A panicking handler must not take the plugin process with it, which would
// leave every other route dialing a socket with no listener.
func TestGRPCBridge_PanicIsContained(t *testing.T) {
	binPath := testplugins.Build(t, "grpccrash")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	before := env.Get(t, "/apps/grpccrash/ping", alice.Auth).Status(200).ReadBody()

	// The panic reaches the user as a plain 500, not as gRPC internals.
	body := env.Get(t, "/apps/grpccrash/panic", alice.Auth).Status(500).ReadBody()
	assert.NotContains(t, body, "rpc error")
	assert.NotContains(t, body, "plugin.sock")

	// Same process ID: it survived the panic and still serves its other routes.
	after := env.Get(t, "/apps/grpccrash/ping", alice.Auth).Status(200).ReadBody()
	assert.Equal(t, before, after)

	// The log buffer is what the admin log page reads.
	entry := waitForLogEntry(t, env, "panic in plugin handler")
	assert.Equal(t, "grpccrash", entry.Source)
	assert.Contains(t, entry.Message, "handler exploded")
}

// A plugin that exits on its own is reaped, restarted, and serving again, and
// requests arriving while it is down get an outage page rather than a dial error.
func TestGRPCBridge_RestartsAfterExit(t *testing.T) {
	binPath := testplugins.Build(t, "grpccrash")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	before := env.Get(t, "/apps/grpccrash/ping", alice.Auth).Status(200).ReadBody()

	// Losing the process mid-request reads as a temporary outage.
	body := env.Get(t, "/apps/grpccrash/exit", alice.Auth).Status(503).ReadBody()
	assert.Contains(t, body, "temporarily unavailable")
	assert.NotContains(t, body, "plugin.sock")

	// What the process wrote on its way out is captured, attributed to it.
	entry := waitForLogEntry(t, env, "plugin is about to exit")
	assert.Equal(t, "grpccrash", entry.Source)
	waitForLogEntry(t, env, "process plugin exited")

	// The supervisor brings it back on the same mounted routes.
	after := waitForRestart(t, env, alice.Auth, before)
	assert.NotEqual(t, before, after)
	waitForLogEntry(t, env, "process plugin restarted")
}

// waitForRestart polls the plugin's ping route until it answers from a process
// other than the one that reported was.
func waitForRestart(t *testing.T, env *cogtest.Env, auth cogtest.HTTPOpt, was string) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp := env.Get(t, "/apps/grpccrash/ping", auth)
		if resp.StatusCode == http.StatusOK {
			if body := resp.ReadBody(); body != was {
				return body
			}
		} else {
			resp.Body.Close()
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("plugin did not restart within 30s (still answering %q)", was)
	return ""
}

// waitForLogEntry waits for a log entry containing substr. Subprocess output
// arrives asynchronously, so the wait is what makes the assertion meaningful.
func waitForLogEntry(t *testing.T, env *cogtest.Env, substr string) logbuf.Entry {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		for _, e := range env.Providers.LogBuffer.Entries() {
			if strings.Contains(e.Message, substr) {
				return e
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no log entry containing %q", substr)
	return logbuf.Entry{}
}

// The authenticated user has to cross the socket: a handler that asks who is
// calling gets nil otherwise.
func TestGRPCBridge_CarriesUser(t *testing.T) {
	binPath := testplugins.Build(t, "grpcwhoami")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	body := env.Get(t, "/apps/grpcwhoami/whoami", alice.Auth).Status(200).ReadBody()
	assert.Contains(t, body, "username=alice")
	assert.Contains(t, body, "id="+alice.ID)
	assert.Contains(t, body, "helper=alice")
}

// An untrusted process plugin has no database. It must be told so, not left to
// dereference a provider that was never injected.
func TestGRPCBridge_UntrustedDBReportsError(t *testing.T) {
	binPath := testplugins.Build(t, "grpcwhoami")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	body := env.Get(t, "/apps/grpcwhoami/db", alice.Auth).Status(200).ReadBody()
	assert.Contains(t, body, "err=")
	assert.Contains(t, body, "not available for gRPC plugins")
}

// A trusted process plugin opens the user's database through the same layout
// and settings the host uses.
func TestGRPCBridge_TrustedDBOpens(t *testing.T) {
	binPath := testplugins.Build(t, "grpcwhoami")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternalTrusted(binPath))

	body := env.Get(t, "/apps/grpcwhoami/db", alice.Auth).Status(200).ReadBody()
	assert.Equal(t, "ok=1", body)
}

// A browser form POST carries its CSRF token in the body, so the host has to
// parse the body to check it. The body still has to reach the plugin: reading
// it here once left the plugin with nothing and every form field empty.
func TestGRPCBridge_FormBodySurvivesCSRFCheck(t *testing.T) {
	binPath := testplugins.Build(t, "grpcform")
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)

	form := url.Values{
		"csrf_token": {auth.CSRFTokenFor(sess.ID, "grpcform", env.Providers.CSRFSecret)},
		"field":      {"kept"},
	}
	body := env.PostForm(t, "/apps/grpcform/echo", form, cookie).Status(200).ReadBody()
	assert.Equal(t, `field="kept"`, body)

	// A process plugin's forms carry a token bound to the plugin. The
	// session-wide token, which would let the plugin's pages post to the
	// host, is not accepted in its place.
	form.Set("csrf_token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))
	env.PostForm(t, "/apps/grpcform/echo", form, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
}
