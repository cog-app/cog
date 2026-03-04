package volume

import (
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
)

type fileBackend struct {
	root string
}

func newFileBackend(root string) *fileBackend {
	return &fileBackend{root: root}
}

func (b *fileBackend) resolve(path string) (string, error) {
	cleaned := filepath.Clean("/" + path)
	full := filepath.Join(b.root, cleaned)
	// Ensure we don't escape the root.
	if !strings.HasPrefix(full, filepath.Clean(b.root)) {
		return "", fmt.Errorf("path traversal detected")
	}
	return full, nil
}

func (b *fileBackend) List(path string) ([]FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("list directory: %w", err)
	}
	var items []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		relPath := filepath.Join(path, e.Name())
		fi := FileInfo{
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

func (b *fileBackend) Stat(path string) (*FileInfo, error) {
	full, err := b.resolve(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	fi := &FileInfo{
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
