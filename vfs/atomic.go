package vfs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// atomicTempSuffix ends the name of a file WriteFileAtomic is still writing.
const atomicTempSuffix = ".cog-partial"

// IsPartial reports whether name is a file WriteFileAtomic is still writing,
// which a directory listing should not show: it is not a file yet, and in a
// moment it will be gone.
func IsPartial(name string) bool {
	return strings.HasPrefix(name, ".") && strings.HasSuffix(name, atomicTempSuffix)
}

// WriteFileAtomic writes content to the local file path, replacing whatever
// is there in one step: the bytes go to a temporary file beside it, which is
// then renamed over path. Writing in place instead — truncate, then copy —
// lets two writers of the same file interleave, and lets a reader see one
// half written; after a rename every reader sees one writer's whole file.
func WriteFileAtomic(path string, content io.Reader, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*"+atomicTempSuffix)
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmp := f.Name()
	fail := func(err error) error {
		return errors.Join(err, f.Close(), os.Remove(tmp))
	}
	if _, err := io.Copy(f, content); err != nil {
		return fail(fmt.Errorf("write file: %w", err))
	}
	if err := f.Chmod(perm); err != nil {
		return fail(fmt.Errorf("set file mode: %w", err))
	}
	if err := f.Close(); err != nil {
		return errors.Join(fmt.Errorf("close file: %w", err), os.Remove(tmp))
	}
	if err := os.Rename(tmp, path); err != nil {
		return errors.Join(fmt.Errorf("replace file: %w", err), os.Remove(tmp))
	}
	return nil
}
