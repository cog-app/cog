package db

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// missingLocks returns those of paths the process holds no POSIX lock on,
// by /proc/locks. A path that no longer exists is skipped.
//
// A lock is matched by inode alone. /proc/locks names the filesystem's
// device, but stat on a btrfs subvolume gives the subvolume's own, so
// matching the device too reported every database on btrfs as unlocked.
// Two files this process locks would have to share an inode on different
// filesystems to be confused.
func missingLocks(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	data, err := os.ReadFile("/proc/locks")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errLockCheckUnsupported
	}
	if err != nil {
		return nil, fmt.Errorf("read held locks: %w", err)
	}
	held, err := lockedInodes(string(data), strconv.Itoa(os.Getpid()))
	if err != nil {
		return nil, err
	}
	var missing []string
	for _, p := range paths {
		var st unix.Stat_t
		if err := unix.Stat(p, &st); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		if !held[st.Ino] {
			missing = append(missing, p)
		}
	}
	return missing, nil
}

// lockedInodes returns the inodes of the files pid holds a POSIX lock on,
// from the contents of /proc/locks.
func lockedInodes(procLocks, pid string) (map[uint64]bool, error) {
	// "1: POSIX  ADVISORY  READ 1234 08:02:26742684 128 128"
	held := map[uint64]bool{}
	for line := range strings.SplitSeq(procLocks, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[1] != "POSIX" || f[4] != pid {
			continue
		}
		i := strings.LastIndexByte(f[5], ':')
		ino, err := strconv.ParseUint(f[5][i+1:], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("read held locks: %q: %w", line, err)
		}
		held[ino] = true
	}
	return held, nil
}
