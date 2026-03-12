// Package vfs defines the virtual filesystem interfaces and types used
// throughout Cog. It has zero internal dependencies so any package —
// including public plugin code — can import it without cycles.
package vfs

import (
	"errors"
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

// AttrSetter supports chmod/chtimes/truncate/link (SFTP Setstat, etc.).
type AttrSetter interface {
	Chmod(path string, mode os.FileMode) error
	Chtimes(path string, atime, mtime time.Time) error
	Truncate(path string, size int64) error
	Link(oldname, newname string) error
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
