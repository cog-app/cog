package plugin_test

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// pingPluginSrc is a self-contained external plugin used to smoke-test the
// gRPC plugin bridge. It registers GET /apps/grpcping/ping and returns "pong".
const pingPluginSrc = `package main

import (
	"context"
	"net/http"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type pingPlugin struct{}

func (p *pingPlugin) ID() string { return "grpcping" }

func (p *pingPlugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterAPI("ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("pong"))
	}))
	return nil
}

func (p *pingPlugin) Start(_ context.Context) error { return nil }
func (p *pingPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&pingPlugin{}) }
`

// crashPluginSrc is an external plugin with routes that fail in the two ways a
// process plugin can take itself down: a panic in a handler, and an outright
// exit. /ping reports the process ID so a test can tell a restarted process
// from the original.
const crashPluginSrc = `package main

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/plugcmd"
)

type crashPlugin struct{}

func (p *crashPlugin) ID() string { return "grpccrash" }

func (p *crashPlugin) Register(ctx plugin.PluginContext) error {
	ctx.RegisterAPI("ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "pong %d", os.Getpid())
	}))
	ctx.RegisterAPI("panic", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded")
	}))
	ctx.RegisterAPI("exit", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(os.Stderr, "plugin is about to exit")
		os.Exit(7)
	}))
	return nil
}

func (p *crashPlugin) Start(_ context.Context) error { return nil }
func (p *crashPlugin) Stop() error                   { return nil }

func main() { plugcmd.Run(&crashPlugin{}) }
`

// buildPingPlugin builds the ping plugin binary. See buildPlugin.
func buildPingPlugin(t *testing.T) string {
	t.Helper()
	return buildPlugin(t, "grpcping", pingPluginSrc)
}

// buildPlugin writes src to a temp package under the module root and runs
// `go build`, returning the binary path. Skips the test if `go` isn't on PATH.
func buildPlugin(t *testing.T, id, src string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	// Place the source under the module root so it sees the same go.mod.
	repoRoot := findRepoRoot(t)
	pkgDir := filepath.Join(repoRoot, "internal", "plugin", ".grpctest-tmp-"+id)
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	t.Cleanup(func() { os.RemoveAll(pkgDir) })

	srcPath := filepath.Join(pkgDir, "main.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(src), 0o644))

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "cog-plugin-"+id)

	cmd := exec.Command("go", "build", "-o", binPath, "./internal/plugin/"+filepath.Base(pkgDir))
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build %s plugin: %v\n%s", id, err, out)
	}
	return binPath
}

// findRepoRoot walks up from the current working directory looking for go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func TestGRPCBridge_RouteServes(t *testing.T) {
	// Build the external plugin binary.
	binPath := buildPingPlugin(t)

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
	binPath := buildPingPlugin(t)
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
	binPath := buildPlugin(t, "grpccrash", crashPluginSrc)
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
	binPath := buildPlugin(t, "grpccrash", crashPluginSrc)
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
