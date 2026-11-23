// Package atomicfile replaces files whole: a reader, or a server starting
// after a crash or a power cut, finds the old content or the new, never a
// mix or an empty file.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile replaces path with data, with permissions perm. It writes a
// temporary file beside path, flushes it to disk, and renames it over
// path.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	tmp := f.Name()
	fail := func(err error, more ...error) error {
		return errors.Join(append([]error{fmt.Errorf("write %s: %w", filepath.Base(path), err)}, append(more, os.Remove(tmp))...)...)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err, f.Close())
	}
	if err := f.Chmod(perm); err != nil {
		return fail(err, f.Close())
	}
	if err := f.Sync(); err != nil {
		return fail(err, f.Close())
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(err)
	}
	return nil
}
