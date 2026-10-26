//go:build unix

package webtest

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive lock on path without waiting.
func tryLock(path string) (func(), bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, err
	}
	// Closing the file drops the lock.
	return func() { f.Close() }, true, nil
}
