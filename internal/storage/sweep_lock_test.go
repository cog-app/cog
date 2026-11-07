package storage_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// The system database may live in the data directory (COG_DB=/data/system.db
// in the image). The sweep must not open it: on Linux, a file opened and
// closed outside SQLite drops every lock the process holds on it, and
// another process (the gallery's scan worker) then deletes the -wal and
// -shm files under the server, which fails every query after with "disk
// I/O error: no such file or directory".
func TestSweepCachesKeepsDatabaseLocks(t *testing.T) {
	if _, err := os.Stat("/proc/locks"); err != nil {
		t.Skip("no /proc/locks to read locks from")
	}
	s, dir, _ := newStore(t, "alice")
	path := filepath.Join(dir, "system.db")
	sdb, err := db.OpenWithHooks(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sdb.Close()) })
	_, err = sdb.Exec(`CREATE TABLE t (x)`)
	require.NoError(t, err)
	require.True(t, holdsLock(t, path), "no lock before the sweep")

	_, err = s.SweepCaches(time.Hour)
	require.NoError(t, err)
	require.True(t, holdsLock(t, path), "the sweep dropped this process's lock on the database")
}

// holdsLock reports whether this process holds a POSIX lock on path, by
// /proc/locks.
func holdsLock(t *testing.T, path string) bool {
	t.Helper()
	var st syscall.Stat_t
	require.NoError(t, syscall.Stat(path, &st))
	locks, err := os.ReadFile("/proc/locks")
	require.NoError(t, err)
	pid := fmt.Sprintf(" %d ", os.Getpid())
	ino := fmt.Sprintf(":%d ", st.Ino)
	for line := range strings.SplitSeq(string(locks), "\n") {
		if strings.Contains(line, "POSIX") && strings.Contains(line, pid) && strings.Contains(line, ino) {
			return true
		}
	}
	return false
}
