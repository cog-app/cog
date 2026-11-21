// Package vfs defines the virtual filesystem interfaces and types used
// throughout Cog. It has zero internal dependencies so any package —
// including public plugin code — can import it without cycles.
package vfs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"
)

// FS is the core VFS interface — a read-write filesystem abstraction.
type FS interface {
	List(path string) ([]FileInfo, error)
	Stat(path string) (*FileInfo, error)
	Get(path string) (io.ReadCloser, error)
	Put(path string, content io.Reader) error
	Mkdir(path string) error
	Delete(path string) error
	Rename(oldPath, newPath string) error
	ReadFile(path string) ([]byte, error)
}

// FileInfo describes a file or directory.
type FileInfo struct {
	Name     string
	Path     string
	Size     int64
	ModTime  time.Time
	IsDir    bool
	MimeType string
}

// Unwrapper is implemented by mount-routing FS implementations.
// Unwrap resolves a path to the underlying FS and the subpath within it.
type Unwrapper interface {
	Unwrap(path string) (fsys FS, subpath string, err error)
}

// FileOpener supports opening files with specific flags (SFTP, SCP, shell redirects).
type FileOpener interface {
	OpenFile(path string, flag int, perm os.FileMode) (File, error)
}

// File is a random-access file handle returned by FileOpener.
type File interface {
	io.Reader
	io.Writer
	io.ReaderAt
	io.WriterAt
	io.Seeker
	io.Closer
	Stat() (fs.FileInfo, error)
	Truncate(size int64) error
}

// AtomicCreator writes a file whole: what is written to the file it returns
// appears at a path, replacing what is there, only when it is committed.
type AtomicCreator interface {
	CreateAtomic(path string, perm os.FileMode) (PendingFile, error)
}

// PendingFile is a file CreateAtomic is writing. Closing it neither commits
// nor discards it.
type PendingFile interface {
	File
	// Commit closes the file if it is open and puts it at path: the path it
	// was created for, or another the FS can move it to in one step.
	Commit(path string) error
	// Abort closes the file if it is open and discards it.
	Abort() error
}

// AttrSetter supports chmod/chtimes/truncate/link (SFTP Setstat, etc.).
type AttrSetter interface {
	Chmod(path string, mode os.FileMode) error
	Chtimes(path string, atime, mtime time.Time) error
	Truncate(path string, size int64) error
	Link(oldname, newname string) error
}

// Replacer is implemented by file systems that keep a file's identity
// across an update made by writing a new version beside it and renaming that
// over it, as rsync and many editors do to replace a file atomically.
//
// Rename over an existing file deletes that file and moves another to its
// name; Replace updates the file at newPath with oldPath's content. When
// newPath is not an existing file, the two are the same.
type Replacer interface {
	Replace(oldPath, newPath string) error
}

// Replace moves oldPath over newPath as an update of newPath when fsys
// supports it, and renames otherwise.
func Replace(fsys FS, oldPath, newPath string) error {
	if r, ok := fsys.(Replacer); ok {
		return r.Replace(oldPath, newPath)
	}
	return fsys.Rename(oldPath, newPath)
}

// NoReplacer is implemented by file systems that can rename without
// replacing what is at the new path, deciding that in the same step as the
// move, so a file that appears there a moment before is not deleted.
// RenameNoReplace fails with an error matching fs.ErrExist when newPath
// is taken.
type NoReplacer interface {
	RenameNoReplace(oldPath, newPath string) error
}

// RenameNoReplace moves oldPath to newPath unless something is already
// there. A file system that cannot decide that as it moves fails with
// errors.ErrUnsupported rather than check first and race.
func RenameNoReplace(fsys FS, oldPath, newPath string) error {
	if r, ok := fsys.(NoReplacer); ok {
		return r.RenameNoReplace(oldPath, newPath)
	}
	return fmt.Errorf("rename %s without replacing: %w", oldPath, errors.ErrUnsupported)
}

// DirReader provides os-level fs.FileInfo/fs.DirEntry (SFTP, shell stat/readdir).
type DirReader interface {
	StatInfo(path string) (fs.FileInfo, error)
	LstatInfo(path string) (fs.FileInfo, error)
	ReadDirEntries(path string) ([]fs.DirEntry, error)
}

// LocalRooter is optionally implemented by backends backed by a local directory.
// Callers can use this after Unwrap to resolve virtual paths to real filesystem
// paths for external tools that require local file access (EXIF, ffmpeg, etc.).
type LocalRooter interface {
	LocalRoot() string
}

// Standard errors.
var (
	ErrNotFound   = errors.New("not found")
	ErrReadOnly   = errors.New("read-only mount")
	ErrCrossMount = errors.New("operation crosses mount boundaries")
)

// TrashDir is the directory at the root of each mount that holds what was
// trashed from it, so trashing is a rename within one backend. A mount-routing
// FS hides it: only code that reaches the backend through MountRooter sees it.
const TrashDir = ".cog-trash"

// MountRooter is implemented by mount-routing FS implementations.
type MountRooter interface {
	// MountRoot returns the mount holding path: its backend, its root
	// within that backend, the path it is mounted at, and whether it is
	// read-only.
	MountRoot(path string) (fsys FS, root, mountPath string, readOnly bool, err error)
}
