package volume

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	_ vfs.AtomicCreator = (*CollectionFS)(nil)
	_ vfs.AttrSetter    = (*CollectionFS)(nil)
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

// CollectionFS is a flat folder of files from someone's tree, as a plugin
// lists them: an album's photos, say. With Folder and Add set, a new file is
// written to Folder and added to the collection. Its files are the owner's,
// and can be changed, renamed and deleted through it only when Modify is set
// (and, to rename, Remove).
type CollectionFS struct {
	owner vfs.FS
	list  func() ([]string, error)

	Folder func() (string, error)
	Add    func(ownerPath string) error
	Remove func(ownerPath string) error
	Modify bool

	mu      sync.Mutex
	entries []CollectionEntry
	expires time.Time
}

// NewCollectionFS returns the folder of the files list names in owner.
func NewCollectionFS(owner vfs.FS, list func() ([]string, error)) *CollectionFS {
	return &CollectionFS{owner: owner, list: list}
}

// Writable reports whether anything can be written through c.
func (c *CollectionFS) Writable() bool {
	return c.canAdd() || c.Modify
}

func (c *CollectionFS) canAdd() bool { return c.Folder != nil && c.Add != nil }

func (c *CollectionFS) invalidate() {
	c.mu.Lock()
	c.expires = time.Time{}
	c.mu.Unlock()
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
	opener, ok := c.owner.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("backend does not support OpenFile")
	}
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND) == 0 {
		t, err := c.target(p)
		if err != nil {
			return nil, err
		}
		return opener.OpenFile(t, flag, perm)
	}
	if t, ok := c.OwnerPath(p); ok {
		if !c.Modify || flag&os.O_EXCL != 0 {
			return nil, fmt.Errorf("%s: %w", p, vfs.ErrReadOnly)
		}
		return opener.OpenFile(t, flag, perm)
	}
	if flag&os.O_CREATE == 0 {
		if _, err := c.target(p); err != nil {
			return nil, err
		}
	}
	dst, err := c.newFile(p)
	if err != nil {
		return nil, err
	}
	f, err := opener.OpenFile(dst, flag|os.O_EXCL, perm)
	if err != nil {
		return nil, err
	}
	// Added before it is written, so the name answers a stat while it is.
	if err := c.added(dst); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func (c *CollectionFS) Put(p string, content io.Reader) error {
	dst, isNew, err := c.writeTarget(p)
	if err != nil {
		return err
	}
	if err := c.owner.Put(dst, content); err != nil {
		return err
	}
	if isNew {
		return c.added(dst)
	}
	return nil
}

// CreateAtomic creates p where its bytes will go in the owner's files: over
// their file, or a new one in Folder. Commit decides again by the name it is
// given, so a file can be committed under another.
func (c *CollectionFS) CreateAtomic(p string, perm os.FileMode) (vfs.PendingFile, error) {
	ac, ok := c.owner.(vfs.AtomicCreator)
	if !ok {
		return nil, fmt.Errorf("create %s: %w", p, errors.ErrUnsupported)
	}
	dst, _, err := c.writeTarget(p)
	if err != nil {
		return nil, err
	}
	f, err := ac.CreateAtomic(dst, perm)
	if err != nil {
		return nil, err
	}
	return &collectionPending{PendingFile: f, c: c}, nil
}

// writeTarget returns where a write of p goes in the owner's files, and
// whether that is a new file to add to the collection.
func (c *CollectionFS) writeTarget(p string) (string, bool, error) {
	if t, ok := c.OwnerPath(p); ok {
		if !c.Modify {
			return "", false, fmt.Errorf("%s: %w", p, vfs.ErrReadOnly)
		}
		return t, false, nil
	}
	dst, err := c.newFile(p)
	return dst, true, err
}

type collectionPending struct {
	vfs.PendingFile
	c *CollectionFS
}

func (f *collectionPending) Commit(p string) error {
	dst, isNew, err := f.c.writeTarget(p)
	if err != nil {
		return errors.Join(err, f.Abort())
	}
	if err := f.PendingFile.Commit(dst); err != nil {
		return err
	}
	if isNew {
		return f.c.added(dst)
	}
	return nil
}

// flatName returns p as the name of a file in the collection.
func flatName(p string) (string, error) {
	p = path.Clean("/" + p)
	if p == "/" || path.Dir(p) != "/" {
		return "", fmt.Errorf("%s: a collection holds no folders: %w", p, vfs.ErrReadOnly)
	}
	return p[1:], nil
}

// newFile returns where in its owner's files a file added as p goes: in
// Folder, under p's name or, when that is taken there, "name (2).ext".
func (c *CollectionFS) newFile(p string) (string, error) {
	if !c.canAdd() {
		return "", vfs.ErrReadOnly
	}
	name, err := flatName(p)
	if err != nil {
		return "", err
	}
	if _, ok := c.OwnerPath(name); ok {
		return "", fmt.Errorf("%s: %w", p, vfs.ErrReadOnly)
	}
	folder, err := c.Folder()
	if err != nil {
		return "", err
	}
	folder = path.Clean("/" + folder)
	if err := c.owner.Mkdir(folder); err != nil {
		return "", fmt.Errorf("create %s: %w", folder, err)
	}
	return c.freePath(folder, name)
}

func (c *CollectionFS) freePath(folder, base string) (string, error) {
	ext := path.Ext(base)
	name := base
	for n := 2; ; n++ {
		dst := path.Join(folder, name)
		_, err := c.owner.Stat(dst)
		if isNotExist(err) {
			return dst, nil
		}
		if err != nil {
			return "", err
		}
		name = strings.TrimSuffix(base, ext) + " (" + strconv.Itoa(n) + ")" + ext
	}
}

func isNotExist(err error) bool {
	return errors.Is(err, vfs.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// added adds dst, just written, to the collection, or removes it from the
// owner's files when it cannot be.
func (c *CollectionFS) added(dst string) error {
	defer c.invalidate()
	if err := c.Add(dst); err != nil {
		return errors.Join(fmt.Errorf("add %s: %w", dst, err), c.owner.Delete(dst))
	}
	return nil
}

func (c *CollectionFS) Mkdir(p string) error {
	if path.Clean("/"+p) == "/" {
		return nil
	}
	return fmt.Errorf("%s: a collection holds no folders: %w", p, vfs.ErrReadOnly)
}

// Delete deletes the owner's file p.
func (c *CollectionFS) Delete(p string) error {
	if !c.Modify {
		return vfs.ErrReadOnly
	}
	t, err := c.target(p)
	if err != nil {
		return err
	}
	defer c.invalidate()
	return c.owner.Delete(t)
}

// Rename renames the owner's file in its folder, keeping it in the
// collection.
func (c *CollectionFS) Rename(oldPath, newPath string) error {
	if !c.Modify || c.Remove == nil || c.Add == nil {
		return vfs.ErrReadOnly
	}
	from, err := c.target(oldPath)
	if err != nil {
		return err
	}
	name, err := flatName(newPath)
	if err != nil {
		return err
	}
	if _, ok := c.OwnerPath(name); ok {
		return fmt.Errorf("%s: %w", newPath, fs.ErrExist)
	}
	to := path.Join(path.Dir(from), name)
	if _, err := c.owner.Stat(to); err == nil {
		return fmt.Errorf("%s: %w", to, fs.ErrExist)
	} else if !isNotExist(err) {
		return err
	}
	defer c.invalidate()
	if err := c.owner.Rename(from, to); err != nil {
		return err
	}
	if err := c.Add(to); err != nil {
		return errors.Join(fmt.Errorf("add %s: %w", to, err), c.owner.Rename(to, from))
	}
	if err := c.Remove(from); err != nil {
		return fmt.Errorf("remove %s: %w", from, err)
	}
	return nil
}

func (c *CollectionFS) attrSetter() (vfs.AttrSetter, error) {
	as, ok := c.owner.(vfs.AttrSetter)
	if !ok {
		return nil, fmt.Errorf("owner's files: %w", errors.ErrUnsupported)
	}
	return as, nil
}

// Chmod does nothing: Cog's files have no modes of their own.
func (c *CollectionFS) Chmod(p string, _ os.FileMode) error {
	_, err := c.target(p)
	return err
}

// Chtimes sets the times of a file the share may change: any, with Modify;
// otherwise one in Folder, where what was added goes, so a client that keeps
// a file's time (rsync -a) can add one.
func (c *CollectionFS) Chtimes(p string, atime, mtime time.Time) error {
	t, err := c.target(p)
	if err != nil {
		return err
	}
	if !c.Modify {
		if !c.canAdd() {
			return vfs.ErrReadOnly
		}
		folder, err := c.Folder()
		if err != nil {
			return err
		}
		if path.Dir(t) != path.Clean("/"+folder) {
			return fmt.Errorf("%s: %w", p, vfs.ErrReadOnly)
		}
	}
	as, err := c.attrSetter()
	if err != nil {
		return err
	}
	return as.Chtimes(t, atime, mtime)
}

func (c *CollectionFS) Truncate(p string, size int64) error {
	if !c.Modify {
		return vfs.ErrReadOnly
	}
	t, err := c.target(p)
	if err != nil {
		return err
	}
	as, err := c.attrSetter()
	if err != nil {
		return err
	}
	return as.Truncate(t, size)
}

func (c *CollectionFS) Link(_, newname string) error {
	return fmt.Errorf("link %s: %w", newname, errors.ErrUnsupported)
}
