// Package testplugins holds the process plugins the host's gRPC bridge is
// tested against, and builds them on demand.
//
// Each plugin is an ordinary main package in a subdirectory rather than a
// string constant in a test file. A string is invisible to the toolchain: it is
// not compiled, vetted, formatted, or refactored with the API it calls, and it
// only fails at the moment a test shells out to build it. These are built by
// `go build ./...` like anything else, so a change to the plugin API breaks them
// where the change is made.
package testplugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Build compiles the named test plugin — the name is its directory, which is
// also its plugin ID — and returns the path to the binary.
//
// It builds rather than `go run`s: the host supervises the process it launched
// and restarts it by path, so a wrapper process in between would be the thing
// under test instead of the plugin. The binary is named cog-plugin-<name> to
// match what process-plugin discovery expects, and because the host labels a
// plugin's log output with that name until GetID tells it the real ID.
func Build(t testing.TB, name string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	binPath := filepath.Join(t.TempDir(), "cog-plugin-"+name)
	pkg := "./internal/testing/testplugins/" + name

	cmd := exec.Command("go", "build", "-o", binPath, pkg)
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test plugin %s: %v\n%s", name, err, out)
	}
	return binPath
}

// repoRoot walks up from the working directory looking for go.mod, so a test
// can build by package path from whichever package directory it runs in.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
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
