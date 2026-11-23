//go:build !windows

package update

import (
	"fmt"
	"os"
	"syscall"
)

// keepOwner gives the new binary at path the owner of the one it replaces,
// of. Root running `cog update` on a system install would otherwise leave
// the service's binary root's. Only root can give a file away, and anyone
// else's new binary is theirs, as the old one was, so it only acts as root.
func keepOwner(path, of string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	fi, err := os.Stat(of)
	if err != nil {
		return fmt.Errorf("owner of %s: %w", of, err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("owner of %s: no owner in %T", of, fi.Sys())
	}
	if err := os.Chown(path, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("give the new binary the owner of %s: %w", of, err)
	}
	return nil
}
