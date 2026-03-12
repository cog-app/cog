package volume

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

// Compile-time interface checks.
var (
	_ vfs.FS          = (*fileBackend)(nil)
	_ vfs.FileOpener  = (*fileBackend)(nil)
	_ vfs.AttrSetter  = (*fileBackend)(nil)
	_ vfs.DirReader   = (*fileBackend)(nil)
	_ vfs.LocalRooter = (*fileBackend)(nil)
)

type fileBackend struct {
	root string
}

func newFileBackend(root string) *fileBackend {
	return &fileBackend{root: root}
}

// LocalRoot returns the root directory on the local filesystem.
func (b *fileBackend) LocalRoot() string { return b.root }

func (b *fileBackend) resolve(path string) (string, error) {
	cleaned := filepath.Clean("/" + path)
	full := filepath.Join(b.root, cleaned)
	// Ensure we don't escape the root.
	if !strings.HasPrefix(full, filepath.Clean(b.root)) {
		return "", fmt.Errorf("path traversal detected")
	}
	return full, nil
}

func (b *fileBackend) List(path string) ([]vfs.FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	var items []vfs.FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath := filepath.Join(path, e.Name())
		fi := vfs.FileInfo{
			Name:    e.Name(),
			Path:    relPath,
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
		}
		if !e.IsDir() {
			fi.MimeType = mime.TypeByExtension(filepath.Ext(e.Name()))
		}
		items = append(items, fi)
	}
	return items, nil
}

func (b *fileBackend) Stat(path string) (*vfs.FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	fi := &vfs.FileInfo{
		Name:    info.Name(),
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}
	if !info.IsDir() {
		fi.MimeType = mime.TypeByExtension(filepath.Ext(info.Name()))
	}
	return fi, nil
}

func (b *fileBackend) Get(path string) (io.ReadCloser, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Open(full)
}

func (b *fileBackend) Put(path string, content io.Reader) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		return fmt.Errorf("create parent dirs: %w", err)
	}
	f, err := os.Create(full)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(f, content); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

func (b *fileBackend) Mkdir(path string) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(full, 0700)
}

func (b *fileBackend) Delete(path string) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	return os.RemoveAll(full)
}

func (b *fileBackend) Rename(oldPath, newPath string) error {
	oldFull, err := b.resolve(oldPath)
	if err != nil {
		return err
	}
	newFull, err := b.resolve(newPath)
	if err != nil {
		return err
	}
	return os.Rename(oldFull, newFull)
}

func (b *fileBackend) ReadFile(path string) ([]byte, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(full)
}

// --- vfs.FileOpener ---

func (b *fileBackend) OpenFile(path string, flag int, perm os.FileMode) (vfs.File, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.OpenFile(full, flag, perm)
}

// --- vfs.AttrSetter ---

func (b *fileBackend) Chmod(path string, mode os.FileMode) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	return os.Chmod(full, mode)
}

func (b *fileBackend) Chtimes(path string, atime, mtime time.Time) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	return os.Chtimes(full, atime, mtime)
}

func (b *fileBackend) Truncate(path string, size int64) error {
	full, err := b.resolve(path)
	if err != nil {
		return err
	}
	return os.Truncate(full, size)
}

func (b *fileBackend) Link(oldname, newname string) error {
	oldFull, err := b.resolve(oldname)
	if err != nil {
		return err
	}
	newFull, err := b.resolve(newname)
	if err != nil {
		return err
	}
	return os.Link(oldFull, newFull)
}

// --- vfs.DirReader ---

func (b *fileBackend) StatInfo(path string) (fs.FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Stat(full)
}

func (b *fileBackend) LstatInfo(path string) (fs.FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.Lstat(full)
}

func (b *fileBackend) ReadDirEntries(path string) ([]fs.DirEntry, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	return os.ReadDir(full)
}
