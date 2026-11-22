package vfs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"time"
)

// Sub returns the part of fsys beneath dir as a filesystem of its own:
// its "/" is dir, and no path reaches outside it. A read-only Sub refuses
// every change.
func Sub(fsys FS, dir string, readOnly bool) FS {
	return &subFS{fs: fsys, dir: path.Clean("/" + dir), readOnly: readOnly}
}

type subFS struct {
	fs       FS
	dir      string
	readOnly bool
}

var (
	_ FileOpener = (*subFS)(nil)
	_ DirReader  = (*subFS)(nil)
	_ AttrSetter = (*subFS)(nil)
	_ Replacer   = (*subFS)(nil)
)

// at is p in the parent filesystem. Cleaning p as an absolute path first
// keeps ".." from climbing out of dir.
func (s *subFS) at(p string) string {
	return path.Join(s.dir, path.Clean("/"+p))
}

func (s *subFS) write(p string) (string, error) {
	if s.readOnly {
		return "", fmt.Errorf("%s: %w", p, ErrReadOnly)
	}
	return s.at(p), nil
}

// inside rewrites a FileInfo's path from the parent's to the Sub's.
func (s *subFS) inside(p string) string {
	rel := path.Clean("/" + p)
	if s.dir != "/" {
		rel = path.Clean("/" + p[min(len(s.dir), len(p)):])
	}
	return rel
}

func (s *subFS) List(p string) ([]FileInfo, error) {
	items, err := s.fs.List(s.at(p))
	for i := range items {
		items[i].Path = s.inside(items[i].Path)
	}
	return items, err
}

func (s *subFS) Stat(p string) (*FileInfo, error) {
	fi, err := s.fs.Stat(s.at(p))
	if err != nil {
		return nil, err
	}
	fi.Path = s.inside(fi.Path)
	if path.Clean("/"+p) == "/" {
		fi.Name = "/"
	}
	return fi, nil
}

func (s *subFS) Get(p string) (io.ReadCloser, error) { return s.fs.Get(s.at(p)) }
func (s *subFS) ReadFile(p string) ([]byte, error)   { return s.fs.ReadFile(s.at(p)) }

func (s *subFS) Put(p string, content io.Reader) error {
	full, err := s.write(p)
	if err != nil {
		return err
	}
	return s.fs.Put(full, content)
}

func (s *subFS) Mkdir(p string) error {
	full, err := s.write(p)
	if err != nil {
		return err
	}
	return s.fs.Mkdir(full)
}

func (s *subFS) Delete(p string) error {
	if path.Clean("/"+p) == "/" {
		return fmt.Errorf("delete /: %w", fs.ErrPermission)
	}
	full, err := s.write(p)
	if err != nil {
		return err
	}
	return s.fs.Delete(full)
}

func (s *subFS) Rename(oldPath, newPath string) error {
	from, err := s.write(oldPath)
	if err != nil {
		return err
	}
	if from == s.dir {
		return fmt.Errorf("rename /: %w", fs.ErrPermission)
	}
	return s.fs.Rename(from, s.at(newPath))
}

func (s *subFS) RenameNoReplace(oldPath, newPath string) error {
	from, err := s.write(oldPath)
	if err != nil {
		return err
	}
	if from == s.dir {
		return fmt.Errorf("rename /: %w", fs.ErrPermission)
	}
	return RenameNoReplace(s.fs, from, s.at(newPath))
}

func (s *subFS) Replace(oldPath, newPath string) error {
	from, err := s.write(oldPath)
	if err != nil {
		return err
	}
	if from == s.dir {
		return fmt.Errorf("rename /: %w", fs.ErrPermission)
	}
	return Replace(s.fs, from, s.at(newPath))
}

func (s *subFS) OpenFile(p string, flag int, perm os.FileMode) (File, error) {
	opener, ok := s.fs.(FileOpener)
	if !ok {
		return nil, fmt.Errorf("open %s: unsupported", p)
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 && s.readOnly {
		return nil, fmt.Errorf("%s: %w", p, ErrReadOnly)
	}
	return opener.OpenFile(s.at(p), flag, perm)
}

func (s *subFS) dirReader() (DirReader, error) {
	dr, ok := s.fs.(DirReader)
	if !ok {
		return nil, fmt.Errorf("read directory: unsupported")
	}
	return dr, nil
}

func (s *subFS) StatInfo(p string) (fs.FileInfo, error) {
	dr, err := s.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.StatInfo(s.at(p))
}

func (s *subFS) LstatInfo(p string) (fs.FileInfo, error) {
	dr, err := s.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.LstatInfo(s.at(p))
}

func (s *subFS) ReadDirEntries(p string) ([]fs.DirEntry, error) {
	dr, err := s.dirReader()
	if err != nil {
		return nil, err
	}
	return dr.ReadDirEntries(s.at(p))
}

func (s *subFS) attrSetter(p string) (AttrSetter, string, error) {
	as, ok := s.fs.(AttrSetter)
	if !ok {
		return nil, "", fmt.Errorf("set attributes: unsupported")
	}
	full, err := s.write(p)
	return as, full, err
}

func (s *subFS) Chmod(p string, mode os.FileMode) error {
	as, full, err := s.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Chmod(full, mode)
}

func (s *subFS) Chtimes(p string, atime, mtime time.Time) error {
	as, full, err := s.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Chtimes(full, atime, mtime)
}

func (s *subFS) Truncate(p string, size int64) error {
	as, full, err := s.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Truncate(full, size)
}

func (s *subFS) Link(oldname, newname string) error {
	as, full, err := s.attrSetter(newname)
	if err != nil {
		return err
	}
	return as.Link(s.at(oldname), full)
}
