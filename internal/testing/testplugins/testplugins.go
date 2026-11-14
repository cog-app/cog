// Package testplugins holds the process plugins the host's gRPC bridge is
// tested against, and builds them on demand. They are ordinary main packages so
// that a change to the plugin API breaks them where it is made, rather than
// inside a string a test compiles at run time.
package testplugins

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Build compiles the named test plugin — the name is its directory, which is
// also its plugin ID — and returns the path to the binary.
//
// It builds rather than `go run`s: the host restarts the process it launched by
// path, so a wrapper in between would be the thing under test.
func Build(t testing.TB, name string) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not in PATH")
	}

	binPath := filepath.Join(t.TempDir(), "cog-plugin-"+name)
	pkg := "./internal/testing/testplugins/" + name
	root := repoRoot(t)

	cmd := exec.Command("go", "build", "-o", binPath, pkg)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build test plugin %s: %v\n%s", name, err, out)
	}
	recordSources(t, root, pkg)
	return binPath
}

// recordSources reads the directory of every package in this module that pkg
// is built from. go test caches a passing result until something the test
// binary was built from, or a file the test opened, changes; a binary built
// by a subprocess is neither, so without this a change to a test plugin, or
// to plugcmd beneath it, would replay the result from before it.
func recordSources(t testing.TB, root, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-deps", "-f", "{{if .Module}}{{if .Module.Main}}{{.Dir}}{{end}}{{end}}", pkg)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("list packages of %s: %v", pkg, err)
	}
	for _, dir := range strings.Fields(string(out)) {
		if _, err := os.ReadDir(dir); err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
	}
}

// repoRoot walks up from the working directory looking for go.mod, so a test
// can build by package path from whichever directory it runs in.
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
