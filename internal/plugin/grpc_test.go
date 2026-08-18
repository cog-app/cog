package plugin_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/testplugins"
)

func TestGRPCBridge_RouteServes(t *testing.T) {
	// Build the external plugin binary.
	binPath := testplugins.Build(t, "grpcping")

	// Spin up a cogtest server. We need to load the external plugin BEFORE
	// the test server starts handling — the registry mux is shared, but
	// LoadExternal also calls plugin.Register which mounts routes on the
	// same mux as web.New, so loading after cogtest.New is acceptable.
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	require.NoError(t, env.Providers.Plugins.LoadExternal(binPath))

	// Hit the plugin's API route.
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

// TestGRPCBridge_PanicIsContained asserts that a panicking handler no longer
// takes the plugin process with it. Before the recovery interceptor, one request
// to a panicking route killed the process and every later request to any of the
// plugin's routes failed with a connection-refused dial error until the host was
// restarted.
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

	// The trace is in the host's log buffer, which is what the admin log page
	// reads — it used to go only to the host's stderr.
	entry := waitForLogEntry(t, env, "panic in plugin handler")
	assert.Equal(t, "grpccrash", entry.Source)
	assert.Contains(t, entry.Message, "handler exploded")
}

// TestGRPCBridge_RestartsAfterExit asserts that a process plugin which exits on
// its own is reaped, restarted, and serving again, and that requests arriving
// while it is down get an outage page rather than a dial error.
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

// TestGRPCBridge_CarriesUser asserts the authenticated user crosses the socket.
// It did not, which is what made recipes panic on every page load: the handler
// called plugin.UserDB, the context held no user, and the nil dereference took
// the process down.
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
