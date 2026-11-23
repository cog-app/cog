package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cog.toml")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))
	require.NoError(t, WriteFile(path, []byte("new"), 0o600))
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new", string(got))
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Equal(t, 1, len(entries), "no temporary file left")

	require.Error(t, WriteFile(filepath.Join(dir, "missing", "x"), nil, 0o600))
}
