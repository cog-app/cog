package db

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// A database whose file something opened and closed outside SQLite has
// lost the process's lock, and the check says so; one left alone does not.
func TestUnlockedFindsLostLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lost.db")
	sdb, err := OpenWithHooks(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sdb.Close()) })
	_, err = sdb.Exec(`CREATE TABLE t (x)`)
	require.NoError(t, err)

	lost, err := unlocked()
	if errors.Is(err, errLockCheckUnsupported) {
		t.Skip(err)
	}
	require.NoError(t, err)
	require.False(t, slices.Contains(lost, path), "reported a database that keeps its lock")

	f, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	lost, err = unlocked()
	require.NoError(t, err)
	require.True(t, slices.Contains(lost, path), "did not report the lost lock: %v", lost)
}
