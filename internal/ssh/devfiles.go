package ssh

import (
	"crypto/rand"
	"io"
	"io/fs"
	"os"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

type devKind int

const (
	devNull devKind = iota
	devZero
	devURandom
)

// devHandler implements vfs.FileHandler for /dev/null, /dev/zero, /dev/urandom.
type devHandler struct {
	kind devKind
	name string
}

func newDevHandler(kind devKind, name string) *devHandler {
	return &devHandler{kind: kind, name: name}
}

func (h *devHandler) Stat() (fs.FileInfo, error) {
	return &devFileInfo{name: h.name}, nil
}

func (h *devHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	return &devFile{kind: h.kind, name: h.name}, nil
}

// devFile implements vfs.File for device files.
type devFile struct {
	kind devKind
	name string
}

func (f *devFile) Read(p []byte) (int, error) {
	switch f.kind {
	case devNull:
		return 0, io.EOF
	case devZero:
		clear(p)
		return len(p), nil
	case devURandom:
		return rand.Read(p)
	}
	return 0, io.EOF
}

func (f *devFile) ReadAt(p []byte, _ int64) (int, error)  { return f.Read(p) }
func (f *devFile) Write(p []byte) (int, error)            { return len(p), nil }
func (f *devFile) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }
func (f *devFile) Seek(_ int64, _ int) (int64, error)     { return 0, nil }
func (f *devFile) Truncate(_ int64) error                 { return nil }
func (f *devFile) Close() error                           { return nil }

func (f *devFile) Stat() (fs.FileInfo, error) {
	return &devFileInfo{name: f.name}, nil
}

// devFileInfo implements fs.FileInfo for device files.
type devFileInfo struct {
	name string
}

func (i *devFileInfo) Name() string       { return i.name }
func (i *devFileInfo) Size() int64        { return 0 }
func (i *devFileInfo) Mode() fs.FileMode  { return 0o666 }
func (i *devFileInfo) ModTime() time.Time { return time.Time{} }
func (i *devFileInfo) IsDir() bool        { return false }
func (i *devFileInfo) Sys() any           { return nil }
