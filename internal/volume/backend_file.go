package volume

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path"
	"path/filepath"
	"syscall"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

// Compile-time interface checks.
var (
	_ vfs.FS              = (*fileBackend)(nil)
	_ vfs.FileOpener      = (*fileBackend)(nil)
	_ vfs.EmptyDirRemover = (*fileBackend)(nil)
	_ vfs.AttrSetter      = (*fileBackend)(nil)
	_ vfs.DirReader       = (*fileBackend)(nil)
	_ vfs.LocalRooter     = (*fileBackend)(nil)
	_ vfs.AtomicCreator   = (*fileBackend)(nil)
	_ vfs.NoReplacer      = (*fileBackend)(nil)
)

// fileBackend serves a local directory. Every operation goes through an
// os.Root opened on that directory, so no name — "..", an absolute path, or
// a symlink someone planted in the tree — reaches a file outside it.
type fileBackend struct {
	root string
}

func newFileBackend(root string) *fileBackend {
	return &fileBackend{root: root}
}

// LocalRoot returns the root directory on the local filesystem.
func (b *fileBackend) LocalRoot() string { return b.root }

// rel turns a volume path ("/a/b", "a/b", "/") into a name within the root.
func rel(p string) string {
	cleaned := path.Clean("/" + filepath.ToSlash(p))
	if cleaned == "/" {
		return "."
	}
	return filepath.FromSlash(cleaned[1:])
}

// with runs fn on the volume's root. A backend is built per request and
// never closed, so the root is opened for each operation rather than held.
func (b *fileBackend) with(fn func(r *os.Root) error) error {
	r, err := os.OpenRoot(b.root)
	if err != nil {
		return fmt.Errorf("open volume: %w", err)
	}
	return errors.Join(fn(r), r.Close())
}

func (b *fileBackend) List(p string) ([]vfs.FileInfo, error) {
	var items []vfs.FileInfo
	err := b.with(func(r *os.Root) error {
		entries, err := fs.ReadDir(r.FS(), filepath.ToSlash(rel(p)))
		if err != nil {
			return fmt.Errorf("list directory: %w", err)
		}
		for _, e := range entries {
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed since ReadDir
			}
			if err != nil {
				return fmt.Errorf("stat %s: %w", e.Name(), err)
			}
			fi := vfs.FileInfo{
				Name:    e.Name(),
				Path:    filepath.Join(p, e.Name()),
				Size:    info.Size(),
				ModTime: info.ModTime(),
				IsDir:   e.IsDir(),
			}
			if !e.IsDir() {
				fi.MimeType = mime.TypeByExtension(filepath.Ext(e.Name()))
			}
			items = append(items, fi)
		}
		return nil
	})
	return items, err
}

func (b *fileBackend) Stat(p string) (*vfs.FileInfo, error) {
	info, err := b.StatInfo(p)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	fi := &vfs.FileInfo{
		Name:    info.Name(),
		Path:    p,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
	if !info.IsDir() {
		fi.MimeType = mime.TypeByExtension(filepath.Ext(info.Name()))
	}
	return fi, nil
}

func (b *fileBackend) Get(p string) (io.ReadCloser, error) {
	var f *os.File
	err := b.with(func(r *os.Root) (err error) {
		f, err = r.Open(rel(p))
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

func (b *fileBackend) Put(p string, content io.Reader) error {
	name := rel(p)
	return b.with(func(r *os.Root) error {
		if err := r.MkdirAll(filepath.Dir(name), 0700); err != nil {
			return fmt.Errorf("create parent dirs: %w", err)
		}
		return vfs.WriteFileAtomicIn(r, name, content, 0o644)
	})
}

func (b *fileBackend) Mkdir(p string) error {
	return b.with(func(r *os.Root) error { return r.MkdirAll(rel(p), 0700) })
}

func (b *fileBackend) Delete(p string) error {
	name := rel(p)
	if name == "." {
		return errors.New("delete: refusing to delete the volume root")
	}
	return b.with(func(r *os.Root) error { return r.RemoveAll(name) })
}

// RemoveEmptyDir is rmdir(2), which refuses a folder with anything in it.
func (b *fileBackend) RemoveEmptyDir(p string) error {
	name := rel(p)
	if name == "." {
		return errors.New("remove: refusing to remove the volume root")
	}
	return b.with(func(r *os.Root) error {
		fi, err := r.Lstat(name)
		if err != nil {
			return err
		}
		if !fi.IsDir() {
			return fmt.Errorf("%s: %w", p, syscall.ENOTDIR)
		}
		err = r.Remove(name)
		if errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST) {
			return fmt.Errorf("%s: %w", p, vfs.ErrNotEmpty)
		}
		return err
	})
}

func (b *fileBackend) Rename(oldPath, newPath string) error {
	return b.with(func(r *os.Root) error { return r.Rename(rel(oldPath), rel(newPath)) })
}

// RenameNoReplace moves oldPath to newPath unless something is there. Unlike
// rename(2), which replaces its target, each way of moving here claims the
// name with a call that fails when it is taken.
func (b *fileBackend) RenameNoReplace(oldPath, newPath string) error {
	return b.with(func(r *os.Root) error {
		from, to := rel(oldPath), rel(newPath)
		fi, err := r.Lstat(from)
		if err != nil {
			return err
		}
		if fi.IsDir() {
			// A folder cannot replace a file or a folder with anything in
			// it, and Go refuses to put one over an empty folder too, so
			// renaming a folder never loses data.
			return r.Rename(from, to)
		}
		// A second name for the file, made only if the name is free, then
		// the first one taken away. Link keeps the file itself, so its
		// times, mode and owner stay as they were.
		err = r.Link(from, to)
		switch {
		case err == nil:
			if err := r.Remove(from); err != nil {
				return errors.Join(fmt.Errorf("remove old name: %w", err), r.Remove(to))
			}
			return nil
		case errors.Is(err, fs.ErrExist), errors.Is(err, fs.ErrNotExist):
			return err
		}
		// No hard links here (FAT, some network and FUSE file systems):
		// claim the name with an empty file and rename over it.
		f, cerr := r.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if cerr != nil {
			return cerr
		}
		if err := f.Close(); err != nil {
			return errors.Join(err, r.Remove(to))
		}
		if err := r.Rename(from, to); err != nil {
			return errors.Join(err, r.Remove(to))
		}
		return nil
	})
}

func (b *fileBackend) ReadFile(p string) ([]byte, error) {
	var data []byte
	err := b.with(func(r *os.Root) (err error) {
		data, err = r.ReadFile(rel(p))
		return err
	})
	return data, err
}

// --- vfs.FileOpener ---

func (b *fileBackend) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	var f *os.File
	err := b.with(func(r *os.Root) (err error) {
		f, err = r.OpenFile(rel(p), flag, perm)
		return err
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// CreateAtomic writes beside p under a name listings hide, and renames
// over p on commit.
func (b *fileBackend) CreateAtomic(p string, _ os.FileMode) (vfs.PendingFile, error) {
	tmp, err := vfs.PartialName(rel(p))
	if err != nil {
		return nil, fmt.Errorf("create temporary file: %w", err)
	}
	var f *os.File
	err = b.with(func(r *os.Root) error {
		if err := r.MkdirAll(filepath.Dir(tmp), 0700); err != nil {
			return fmt.Errorf("create parent dirs: %w", err)
		}
		var err error
		f, err = r.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &filePending{File: f, b: b, tmp: tmp}, nil
}

type filePending struct {
	*os.File
	b      *fileBackend
	tmp    string
	closed bool
}

func (f *filePending) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true
	return f.File.Close()
}

func (f *filePending) Commit(p string) error {
	if err := f.Close(); err != nil {
		return errors.Join(err, f.Abort())
	}
	return f.b.with(func(r *os.Root) error {
		if err := r.MkdirAll(filepath.Dir(rel(p)), 0700); err != nil {
			return errors.Join(fmt.Errorf("create parent dirs: %w", err), r.Remove(f.tmp))
		}
		if err := r.Rename(f.tmp, rel(p)); err != nil {
			return errors.Join(fmt.Errorf("replace file: %w", err), r.Remove(f.tmp))
		}
		return nil
	})
}

func (f *filePending) Abort() error {
	return errors.Join(f.Close(), f.b.with(func(r *os.Root) error { return r.Remove(f.tmp) }))
}

// --- vfs.AttrSetter ---

func (b *fileBackend) Chmod(p string, mode os.FileMode) error {
	return b.with(func(r *os.Root) error { return r.Chmod(rel(p), mode) })
}

func (b *fileBackend) Chtimes(p string, atime, mtime time.Time) error {
	return b.with(func(r *os.Root) error { return r.Chtimes(rel(p), atime, mtime) })
}

func (b *fileBackend) Truncate(p string, size int64) error {
	return b.with(func(r *os.Root) error {
		f, err := r.OpenFile(rel(p), os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		return errors.Join(f.Truncate(size), f.Close())
	})
}

func (b *fileBackend) Link(oldname, newname string) error {
	return b.with(func(r *os.Root) error { return r.Link(rel(oldname), rel(newname)) })
}

// --- vfs.DirReader ---

func (b *fileBackend) StatInfo(p string) (fs.FileInfo, error) {
	var info fs.FileInfo
	err := b.with(func(r *os.Root) (err error) {
		info, err = r.Stat(rel(p))
		return err
	})
	return info, err
}

func (b *fileBackend) LstatInfo(p string) (fs.FileInfo, error) {
	var info fs.FileInfo
	err := b.with(func(r *os.Root) (err error) {
		info, err = r.Lstat(rel(p))
		return err
	})
	return info, err
}

func (b *fileBackend) ReadDirEntries(p string) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	err := b.with(func(r *os.Root) (err error) {
		entries, err = fs.ReadDir(r.FS(), filepath.ToSlash(rel(p)))
		return err
	})
	return entries, err
}
