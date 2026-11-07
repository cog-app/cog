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
	// "1: POSIX  ADVISORY  READ 1234 08:02:26742684 128 128"
	pid := strconv.Itoa(os.Getpid())
	held := map[string]bool{}
	for line := range strings.SplitSeq(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 6 && f[1] == "POSIX" && f[4] == pid {
			held[f[5]] = true
		}
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
		id := fmt.Sprintf("%02x:%02x:%d", unix.Major(st.Dev), unix.Minor(st.Dev), st.Ino)
		if !held[id] {
			missing = append(missing, p)
		}
	}
	return missing, nil
}
