package plugin_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

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

// buildPingPlugin writes pingPluginSrc to a temp dir and runs `go build`.
// Returns the binary path. Skips the test if `go` isn't on PATH.
func buildPingPlugin(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	// Place the source under the module root so it sees the same go.mod.
	repoRoot := findRepoRoot(t)
	pkgDir := filepath.Join(repoRoot, "internal", "plugin", ".grpctest-tmp")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	t.Cleanup(func() { os.RemoveAll(pkgDir) })

	srcPath := filepath.Join(pkgDir, "main.go")
	require.NoError(t, os.WriteFile(srcPath, []byte(pingPluginSrc), 0o644))

	binDir := t.TempDir()
	binPath := filepath.Join(binDir, "cog-plugin-grpcping")

	cmd := exec.Command("go", "build", "-o", binPath, "./internal/plugin/.grpctest-tmp")
	cmd.Dir = repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build ping plugin: %v\n%s", err, out)
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
