package db

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// On btrfs, stat gives a subvolume's own device number while /proc/locks
// gives the filesystem's, so a lock counts as held by its inode alone.
func TestLockedInodesIgnoresDevice(t *testing.T) {
	locks := "1: POSIX  ADVISORY  READ 1234 00:34:104651142 128 128\n" +
		"2: POSIX  ADVISORY  READ 999 00:34:7 128 128\n" +
		"3: FLOCK  ADVISORY  WRITE 1234 00:34:8 0 EOF\n"
	held, err := lockedInodes(locks, "1234")
	require.NoError(t, err)
	require.True(t, held[104651142], "missed the process's POSIX lock")
	require.False(t, held[7], "counted another process's lock")
	require.False(t, held[8], "counted a flock, which SQLite does not take")
}
