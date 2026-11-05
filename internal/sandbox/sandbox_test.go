package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestArgs: the program sees the system read-only, its inputs read-only, its
// output directory writable, and no network unless asked.
func TestArgs(t *testing.T) {
	a := argsFor(Spec{Read: []string{"/data/alice/in.svg"}, Write: []string{"/data/alice/out"}}, "/usr/bin/inkscape")
	joined := strings.Join(a, " ")
	for _, want := range []string{
		"--unshare-all", "--clearenv", "--ro-bind-try /usr /usr", "--tmpfs /tmp",
		"--ro-bind /data/alice/in.svg /data/alice/in.svg",
		"--bind /data/alice/out /data/alice/out",
	} {
		require.True(t, strings.Contains(joined, want), "missing %q in %s", want, joined)
	}
	require.False(t, slices.Contains(a, "--share-net"), "network shared without being asked for")
	require.Equal(t, "/usr/bin/inkscape", a[len(a)-1])

	a = argsFor(Spec{Network: true}, "/srv/venv/bin/yt-dlp")
	require.True(t, slices.Contains(a, "--share-net"), "network not shared")
	require.True(t, strings.Contains(strings.Join(a, " "), "--ro-bind /srv/venv /srv/venv"), "a program outside the system dirs lost its install tree")
}

// TestConfined runs a shell in the sandbox where bwrap works: it reads its
// input, writes its output, and cannot see a file beside the input.
func TestConfined(t *testing.T) {
	if err := Status(); err != nil {
		t.Skipf("no sandbox here: %v", err)
	}
	dir := t.TempDir()
	in := filepath.Join(dir, "in.txt")
	secret := filepath.Join(dir, "secret.txt")
	out := filepath.Join(dir, "out")
	require.NoError(t, os.WriteFile(in, []byte("input"), 0o600))
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o600))
	require.NoError(t, os.Mkdir(out, 0o700))

	script := `cat "$1" > "$3/copy" && ! cat "$2" 2>/dev/null`
	cmd := CommandContext(context.Background(), Spec{Read: []string{in}, Write: []string{out}}, "sh", "-c", script, "sh", in, secret, out)
	b, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", b)
	got, err := os.ReadFile(filepath.Join(out, "copy"))
	require.NoError(t, err)
	require.Equal(t, "input", string(got))
}
