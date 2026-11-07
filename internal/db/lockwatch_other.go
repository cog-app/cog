//go:build !linux

package db

// missingLocks cannot read the held locks outside Linux.
func missingLocks(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	return nil, errLockCheckUnsupported
}
