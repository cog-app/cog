//go:build linux || darwin

package service

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// GiveDir makes account the owner of everything in dir.
func GiveDir(dir, account string) error {
	uid, gid, err := lookupIDs(account)
	if err != nil {
		return err
	}
	return filepath.WalkDir(dir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

func lookupIDs(account string) (uid, gid int, err error) {
	u, err := user.Lookup(account)
	if err != nil {
		return 0, 0, fmt.Errorf("look up %s: %w", account, err)
	}
	if uid, err = strconv.Atoi(u.Uid); err != nil {
		return 0, 0, fmt.Errorf("uid of %s: %w", account, err)
	}
	if gid, err = strconv.Atoi(u.Gid); err != nil {
		return 0, 0, fmt.Errorf("gid of %s: %w", account, err)
	}
	return uid, gid, nil
}

// RunAs makes cmd run as account, for the parts of setup that make the
// server's files: they then belong to the account the server runs as.
func RunAs(cmd *exec.Cmd, account string) error {
	uid, gid, err := lookupIDs(account)
	if err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	return nil
}

// IsAdmin reports whether this process may install a system service.
func IsAdmin() bool { return os.Geteuid() == 0 }
