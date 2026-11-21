package volume

import (
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

var (
	_ vfs.FS         = (*CollectionFS)(nil)
	_ vfs.Unwrapper  = (*CollectionFS)(nil)
	_ vfs.FileOpener = (*CollectionFS)(nil)
)

// CollectionEntry is a file in a collection: its name there, and where it is
// in its owner's files.
type CollectionEntry struct {
	Name string
	Path string
}

// CollectionEntries names the files at paths for a flat folder. The names
// are their own, the second "IMG_1.jpg" (by path, so a name stays put while
// the files do) becoming "IMG_1 (2).jpg".
func CollectionEntries(paths []string) []CollectionEntry {
	paths = slices.Clone(paths)
	for i, p := range paths {
		paths[i] = path.Clean("/" + p)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	seen := make(map[string]bool, len(paths))
	out := make([]CollectionEntry, 0, len(paths))
	for _, p := range paths {
		base := path.Base(p)
		name := base
		ext := path.Ext(base)
		for n := 2; seen[strings.ToLower(name)]; n++ {
			name = strings.TrimSuffix(base, ext) + " (" + strconv.Itoa(n) + ")" + ext
		}
		seen[strings.ToLower(name)] = true
		out = append(out, CollectionEntry{Name: name, Path: p})
	}
	return out
}

// collectionTTL is how long a collection's listing is trusted: a scan stats
// every file after listing the folder, and each listing is a query of the
// plugin that keeps it.
const collectionTTL = 2 * time.Second

// CollectionFS is a read-only flat folder of files from someone's tree,
// as a plugin lists them: an album's photos, say.
type CollectionFS struct {
	owner vfs.FS
	list  func() ([]string, error)

	mu      sync.Mutex
	entries []CollectionEntry
	expires time.Time
}

// NewCollectionFS returns the folder of the files list names in owner.
func NewCollectionFS(owner vfs.FS, list func() ([]string, error)) *CollectionFS {
	return &CollectionFS{owner: owner, list: list}
}

func (c *CollectionFS) load() ([]CollectionEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.expires) {
		return c.entries, nil
	}
	paths, err := c.list()
	if err != nil {
		return nil, err
	}
	c.entries, c.expires = CollectionEntries(paths), time.Now().Add(collectionTTL)
	return c.entries, nil
}

// OwnerPath returns where p, a path in the collection, is in its owner's
// files.
func (c *CollectionFS) OwnerPath(p string) (string, bool) {
	p = path.Clean("/" + p)
	if p == "/" || path.Dir(p) != "/" {
		return "", false
	}
	entries, err := c.load()
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.Name == p[1:] {
			return e.Path, true
		}
	}
	return "", false
}

func (c *CollectionFS) target(p string) (string, error) {
	if t, ok := c.OwnerPath(p); ok {
		return t, nil
	}
	if _, err := c.load(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
}

func (c *CollectionFS) List(p string) ([]vfs.FileInfo, error) {
	if path.Clean("/"+p) != "/" {
		if _, err := c.target(p); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s: not a directory: %w", p, vfs.ErrNotFound)
	}
	entries, err := c.load()
	if err != nil {
		return nil, err
	}
	out := make([]vfs.FileInfo, 0, len(entries))
	for _, e := range entries {
		fi, err := c.owner.Stat(e.Path)
		if err != nil {
			continue // gone from the owner's files since the plugin last saw it
		}
		if fi.IsDir {
			continue
		}
		fi.Name, fi.Path = e.Name, "/"+e.Name
		out = append(out, *fi)
	}
	return out, nil
}

func (c *CollectionFS) Stat(p string) (*vfs.FileInfo, error) {
	if path.Clean("/"+p) == "/" {
		return &vfs.FileInfo{Name: "/", Path: "/", IsDir: true}, nil
	}
	t, err := c.target(p)
	if err != nil {
		return nil, err
	}
	fi, err := c.owner.Stat(t)
	if err != nil {
		return nil, err
	}
	if fi.IsDir {
		return nil, fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	fi.Name, fi.Path = path.Base(path.Clean("/"+p)), path.Clean("/"+p)
	return fi, nil
}

func (c *CollectionFS) Get(p string) (io.ReadCloser, error) {
	t, err := c.target(p)
	if err != nil {
		return nil, err
	}
	return c.owner.Get(t)
}

func (c *CollectionFS) ReadFile(p string) ([]byte, error) {
	t, err := c.target(p)
	if err != nil {
		return nil, err
	}
	return c.owner.ReadFile(t)
}

// Unwrap resolves p to the backend the owner's file is on, so a program
// that needs a local file (EXIF, ffmpeg) reads the original.
func (c *CollectionFS) Unwrap(p string) (vfs.FS, string, error) {
	t, err := c.target(p)
	if err != nil {
		return nil, "", err
	}
	if u, ok := c.owner.(vfs.Unwrapper); ok {
		return u.Unwrap(t)
	}
	return c.owner, t, nil
}

func (c *CollectionFS) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) != 0 {
		return nil, vfs.ErrReadOnly
	}
	t, err := c.target(p)
	if err != nil {
		return nil, err
	}
	opener, ok := c.owner.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("backend does not support OpenFile")
	}
	return opener.OpenFile(t, flag, perm)
}

func (c *CollectionFS) Put(p string, _ io.Reader) error { return vfs.ErrReadOnly }
func (c *CollectionFS) Mkdir(p string) error            { return vfs.ErrReadOnly }
func (c *CollectionFS) Delete(p string) error           { return vfs.ErrReadOnly }
func (c *CollectionFS) Rename(_, _ string) error        { return vfs.ErrReadOnly }
