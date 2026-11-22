package vfs

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// FileHandler handles a single virtual file in an OverlayFS.
type FileHandler interface {
	Open(flag int, perm os.FileMode) (File, error)
	Stat() (fs.FileInfo, error)
}

// overlayMount routes all paths under a prefix to a mounted FS.
type overlayMount struct {
	prefix string // e.g. "/files"
	fs     FS
}

// OverlayFS wraps a base FS and overlays virtual files registered via Bind.
// The base FS may be nil, in which case the overlay serves as a pure virtual root.
type OverlayFS struct {
	base   FS
	files  map[string]FileHandler // clean absolute paths → handlers
	dirs   map[string]bool        // implicit virtual directories
	mounts []overlayMount         // sorted longest-prefix first
}

// Compile-time interface checks.
var (
	_ FS         = (*OverlayFS)(nil)
	_ FileOpener = (*OverlayFS)(nil)
	_ DirReader  = (*OverlayFS)(nil)
	_ AttrSetter = (*OverlayFS)(nil)
)

// NewOverlayFS creates an OverlayFS wrapping the given base filesystem.
func NewOverlayFS(base FS) *OverlayFS {
	return &OverlayFS{
		base:  base,
		files: make(map[string]FileHandler),
		dirs:  make(map[string]bool),
	}
}

// Bind registers a virtual file at the given absolute path.
// Parent directories are implicitly created.
func (o *OverlayFS) Bind(p string, h FileHandler) {
	p = path.Clean(p)
	o.files[p] = h
	// Register all parent directories as implicit virtual dirs.
	for d := path.Dir(p); d != "/" && d != "."; d = path.Dir(d) {
		o.dirs[d] = true
	}
}

// Mount registers a filesystem at the given prefix. All paths under prefix
// are routed to the mounted FS with the prefix stripped. Parent directories
// up to (but not including) "/" are registered as implicit dirs.
func (o *OverlayFS) Mount(prefix string, fs FS) {
	prefix = path.Clean(prefix)
	o.mounts = append(o.mounts, overlayMount{prefix: prefix, fs: fs})
	// Sort longest-prefix first for correct resolution order.
	sort.Slice(o.mounts, func(i, j int) bool {
		return len(o.mounts[i].prefix) > len(o.mounts[j].prefix)
	})
	// Register parent dirs (not / itself).
	for d := path.Dir(prefix); d != "/" && d != "."; d = path.Dir(d) {
		o.dirs[d] = true
	}
}

// resolveMount checks if p falls under a mount prefix. Returns the mounted FS,
// the subpath within it, and whether a mount matched.
func (o *OverlayFS) resolveMount(p string) (FS, string, bool) {
	for _, m := range o.mounts {
		if p == m.prefix {
			return m.fs, "/", true
		}
		if strings.HasPrefix(p, m.prefix+"/") {
			sub := strings.TrimPrefix(p, m.prefix)
			return m.fs, sub, true
		}
	}
	return nil, "", false
}

func (o *OverlayFS) clean(p string) string {
	return path.Clean("/" + p)
}

// --- vfs.FS ---

func (o *OverlayFS) Stat(p string) (*FileInfo, error) {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		fi, err := h.Stat()
		if err != nil {
			return nil, err
		}
		return &FileInfo{
			Name:    fi.Name(),
			Path:    p,
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
			IsDir:   fi.IsDir(),
		}, nil
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.Stat(sub)
	}
	if o.dirs[p] || (p == "/" && o.base == nil) {
		return &FileInfo{
			Name:  path.Base(p),
			Path:  p,
			IsDir: true,
		}, nil
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	return o.base.Stat(p)
}

func (o *OverlayFS) Get(p string) (io.ReadCloser, error) {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		h := o.files[p]
		f, err := h.Open(os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, err
		}
		return io.NopCloser(strings.NewReader(string(data))), nil
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.Get(sub)
	}
	if o.dirs[p] {
		return nil, fmt.Errorf("is a directory: %s", p)
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	return o.base.Get(p)
}

func (o *OverlayFS) ReadFile(p string) ([]byte, error) {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		f, err := h.Open(os.O_RDONLY, 0)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return io.ReadAll(f)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.ReadFile(sub)
	}
	if o.dirs[p] {
		return nil, fmt.Errorf("is a directory: %s", p)
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	return o.base.ReadFile(p)
}

func (o *OverlayFS) Put(p string, content io.Reader) error {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		f, err := h.Open(os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, content)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.Put(sub, content)
	}
	if o.dirs[p] {
		return fmt.Errorf("is a directory: %s", p)
	}
	if o.base == nil {
		return ErrNotFound
	}
	return o.base.Put(p, content)
}

func (o *OverlayFS) List(p string) ([]FileInfo, error) {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil, fmt.Errorf("not a directory: %s", p)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.List(sub)
	}
	if o.dirs[p] || (p == "/" && o.base == nil) {
		return o.listImplicitDir(p), nil
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	items, err := o.base.List(p)
	if err != nil {
		return nil, err
	}
	// Augment with overlay entries if this is a parent of implicit dirs.
	return o.augmentList(p, items), nil
}

func (o *OverlayFS) Mkdir(p string) error {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return fmt.Errorf("file exists: %s", p)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.Mkdir(sub)
	}
	if o.dirs[p] {
		return nil // no-op, implicit dir already exists
	}
	if o.base == nil {
		return fmt.Errorf("cannot create directory: %s", p)
	}
	return o.base.Mkdir(p)
}

func (o *OverlayFS) Delete(p string) error {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return fmt.Errorf("cannot delete virtual file: %s", p)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		return mfs.Delete(sub)
	}
	if o.dirs[p] {
		return fmt.Errorf("cannot delete virtual directory: %s", p)
	}
	if o.base == nil {
		return ErrNotFound
	}
	return o.base.Delete(p)
}

func (o *OverlayFS) Rename(oldPath, newPath string) error {
	return o.rename(oldPath, newPath, FS.Rename)
}

// RenameNoReplace is Rename that leaves a file already at newPath alone.
func (o *OverlayFS) RenameNoReplace(oldPath, newPath string) error {
	return o.rename(oldPath, newPath, RenameNoReplace)
}

// rename is Rename or RenameNoReplace, moving with move.
func (o *OverlayFS) rename(oldPath, newPath string, move func(FS, string, string) error) error {
	oldPath = o.clean(oldPath)
	newPath = o.clean(newPath)
	if _, ok := o.files[oldPath]; ok {
		return fmt.Errorf("cannot rename virtual file: %s", oldPath)
	}
	if _, ok := o.files[newPath]; ok {
		return fmt.Errorf("cannot rename to virtual file: %s", newPath)
	}
	if o.dirs[oldPath] || o.dirs[newPath] {
		return fmt.Errorf("cannot rename virtual directory")
	}
	// Both paths in the same mount?
	if mfs, subOld, ok := o.resolveMount(oldPath); ok {
		if _, subNew, ok2 := o.resolveMount(newPath); ok2 {
			return move(mfs, subOld, subNew)
		}
		return fmt.Errorf("cannot rename across mount boundaries")
	}
	if o.base == nil {
		return ErrNotFound
	}
	return move(o.base, oldPath, newPath)
}

// Replace is Rename, passing the update through to a mount that keeps a
// file's identity across one.
func (o *OverlayFS) Replace(oldPath, newPath string) error {
	oldPath = o.clean(oldPath)
	newPath = o.clean(newPath)
	if mfs, subOld, ok := o.resolveMount(oldPath); ok {
		if mfs2, subNew, ok2 := o.resolveMount(newPath); ok2 && mfs2 == mfs {
			if _, virtual := o.files[newPath]; !virtual {
				return Replace(mfs, subOld, subNew)
			}
		}
	}
	return o.Rename(oldPath, newPath)
}

// --- vfs.FileOpener ---

func (o *OverlayFS) OpenFile(p string, flag int, perm os.FileMode) (File, error) {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		return h.Open(flag, perm)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if opener, ok := mfs.(FileOpener); ok {
			return opener.OpenFile(sub, flag, perm)
		}
		return nil, fmt.Errorf("mounted FS does not support OpenFile")
	}
	if o.dirs[p] {
		return nil, fmt.Errorf("is a directory: %s", p)
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	if opener, ok := o.base.(FileOpener); ok {
		return opener.OpenFile(p, flag, perm)
	}
	return nil, fmt.Errorf("base does not support OpenFile")
}

// --- vfs.DirReader ---

func (o *OverlayFS) StatInfo(p string) (fs.FileInfo, error) {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		return h.Stat()
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if dr, ok := mfs.(DirReader); ok {
			return dr.StatInfo(sub)
		}
		fi, err := mfs.Stat(sub)
		if err != nil {
			return nil, err
		}
		return &vfsFileInfoAdapter{fi: fi}, nil
	}
	if o.dirs[p] || (p == "/" && o.base == nil) {
		return &syntheticDirInfo{name: path.Base(p)}, nil
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	if dr, ok := o.base.(DirReader); ok {
		return dr.StatInfo(p)
	}
	fi, err := o.base.Stat(p)
	if err != nil {
		return nil, err
	}
	return &vfsFileInfoAdapter{fi: fi}, nil
}

func (o *OverlayFS) LstatInfo(p string) (fs.FileInfo, error) {
	p = o.clean(p)
	if h, ok := o.files[p]; ok {
		return h.Stat()
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if dr, ok := mfs.(DirReader); ok {
			return dr.LstatInfo(sub)
		}
		fi, err := mfs.Stat(sub)
		if err != nil {
			return nil, err
		}
		return &vfsFileInfoAdapter{fi: fi}, nil
	}
	if o.dirs[p] || (p == "/" && o.base == nil) {
		return &syntheticDirInfo{name: path.Base(p)}, nil
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	if dr, ok := o.base.(DirReader); ok {
		return dr.LstatInfo(p)
	}
	fi, err := o.base.Stat(p)
	if err != nil {
		return nil, err
	}
	return &vfsFileInfoAdapter{fi: fi}, nil
}

func (o *OverlayFS) ReadDirEntries(p string) ([]fs.DirEntry, error) {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil, fmt.Errorf("not a directory: %s", p)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if dr, ok := mfs.(DirReader); ok {
			return dr.ReadDirEntries(sub)
		}
		items, err := mfs.List(sub)
		if err != nil {
			return nil, err
		}
		var entries []fs.DirEntry
		for i := range items {
			entries = append(entries, &vfsDirEntryAdapter{fi: &items[i]})
		}
		return entries, nil
	}
	if o.dirs[p] || (p == "/" && o.base == nil) {
		return o.listImplicitDirEntries(p), nil
	}
	if o.base == nil {
		return nil, ErrNotFound
	}
	// Read base entries and augment with overlay children.
	var entries []fs.DirEntry
	if dr, ok := o.base.(DirReader); ok {
		var err error
		entries, err = dr.ReadDirEntries(p)
		if err != nil {
			return nil, err
		}
	} else {
		items, err := o.base.List(p)
		if err != nil {
			return nil, err
		}
		for i := range items {
			entries = append(entries, &vfsDirEntryAdapter{fi: &items[i]})
		}
	}
	return o.augmentDirEntries(p, entries), nil
}

// --- vfs.AttrSetter ---

func (o *OverlayFS) Chmod(p string, mode os.FileMode) error {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil // no-op for virtual files
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if setter, ok := mfs.(AttrSetter); ok {
			return setter.Chmod(sub, mode)
		}
		return nil
	}
	if o.dirs[p] {
		return nil
	}
	if o.base == nil {
		return nil
	}
	if setter, ok := o.base.(AttrSetter); ok {
		return setter.Chmod(p, mode)
	}
	return fmt.Errorf("base does not support Chmod")
}

func (o *OverlayFS) Chtimes(p string, atime, mtime time.Time) error {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if setter, ok := mfs.(AttrSetter); ok {
			return setter.Chtimes(sub, atime, mtime)
		}
		return nil
	}
	if o.dirs[p] {
		return nil
	}
	if o.base == nil {
		return nil
	}
	if setter, ok := o.base.(AttrSetter); ok {
		return setter.Chtimes(p, atime, mtime)
	}
	return fmt.Errorf("base does not support Chtimes")
}

func (o *OverlayFS) Truncate(p string, size int64) error {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if setter, ok := mfs.(AttrSetter); ok {
			return setter.Truncate(sub, size)
		}
		return nil
	}
	if o.dirs[p] {
		return nil
	}
	if o.base == nil {
		return nil
	}
	if setter, ok := o.base.(AttrSetter); ok {
		return setter.Truncate(p, size)
	}
	return fmt.Errorf("base does not support Truncate")
}

func (o *OverlayFS) Link(oldname, newname string) error {
	oldname = o.clean(oldname)
	newname = o.clean(newname)
	if _, ok := o.files[oldname]; ok {
		return fmt.Errorf("cannot link virtual file: %s", oldname)
	}
	if _, ok := o.files[newname]; ok {
		return fmt.Errorf("cannot link to virtual file: %s", newname)
	}
	if mfs, subOld, ok := o.resolveMount(oldname); ok {
		if setter, ok := mfs.(AttrSetter); ok {
			if _, subNew, ok2 := o.resolveMount(newname); ok2 {
				return setter.Link(subOld, subNew)
			}
		}
		return fmt.Errorf("cannot link across mount boundaries")
	}
	if o.base == nil {
		return fmt.Errorf("base does not support Link")
	}
	if setter, ok := o.base.(AttrSetter); ok {
		return setter.Link(oldname, newname)
	}
	return fmt.Errorf("base does not support Link")
}

// --- vfs.Unwrapper (delegate) ---

func (o *OverlayFS) Unwrap(p string) (FS, string, error) {
	p = o.clean(p)
	if _, ok := o.files[p]; ok {
		return nil, "", fmt.Errorf("cannot unwrap virtual file: %s", p)
	}
	if mfs, sub, ok := o.resolveMount(p); ok {
		if u, ok := mfs.(Unwrapper); ok {
			return u.Unwrap(sub)
		}
		return mfs, sub, nil
	}
	if o.dirs[p] {
		return nil, "", fmt.Errorf("cannot unwrap virtual directory: %s", p)
	}
	if o.base == nil {
		return nil, "", fmt.Errorf("no base filesystem")
	}
	if u, ok := o.base.(Unwrapper); ok {
		return u.Unwrap(p)
	}
	return o.base, p, nil
}

// --- vfs.LocalRooter (delegate) ---

func (o *OverlayFS) LocalRoot() string {
	if o.base != nil {
		if lr, ok := o.base.(LocalRooter); ok {
			return lr.LocalRoot()
		}
	}
	return ""
}

// --- helpers ---

// listImplicitDir returns FileInfo entries for children of an implicit virtual directory.
func (o *OverlayFS) listImplicitDir(dir string) []FileInfo {
	var items []FileInfo
	seen := make(map[string]bool)
	for fp, h := range o.files {
		if path.Dir(fp) == dir {
			name := path.Base(fp)
			if seen[name] {
				continue
			}
			seen[name] = true
			fi, err := h.Stat()
			if err != nil {
				continue
			}
			items = append(items, FileInfo{
				Name:    name,
				Path:    fp,
				Size:    fi.Size(),
				ModTime: fi.ModTime(),
				IsDir:   fi.IsDir(),
			})
		}
	}
	for d := range o.dirs {
		if path.Dir(d) == dir {
			name := path.Base(d)
			if seen[name] {
				continue
			}
			seen[name] = true
			items = append(items, FileInfo{
				Name:  name,
				Path:  d,
				IsDir: true,
			})
		}
	}
	// Add mount points whose parent is this directory.
	for _, m := range o.mounts {
		if path.Dir(m.prefix) == dir {
			name := path.Base(m.prefix)
			if seen[name] {
				continue
			}
			seen[name] = true
			items = append(items, FileInfo{
				Name:  name,
				Path:  m.prefix,
				IsDir: true,
			})
		}
	}
	return items
}

// listImplicitDirEntries returns DirEntry entries for children of an implicit virtual directory.
func (o *OverlayFS) listImplicitDirEntries(dir string) []fs.DirEntry {
	var entries []fs.DirEntry
	seen := make(map[string]bool)
	for fp, h := range o.files {
		if path.Dir(fp) == dir {
			name := path.Base(fp)
			if seen[name] {
				continue
			}
			seen[name] = true
			fi, err := h.Stat()
			if err != nil {
				continue
			}
			entries = append(entries, &staticDirEntry{info: fi})
		}
	}
	for d := range o.dirs {
		if path.Dir(d) == dir {
			name := path.Base(d)
			if seen[name] {
				continue
			}
			seen[name] = true
			entries = append(entries, &staticDirEntry{info: &syntheticDirInfo{name: name}})
		}
	}
	// Add mount points whose parent is this directory.
	for _, m := range o.mounts {
		if path.Dir(m.prefix) == dir {
			name := path.Base(m.prefix)
			if seen[name] {
				continue
			}
			seen[name] = true
			entries = append(entries, &staticDirEntry{info: &syntheticDirInfo{name: name}})
		}
	}
	return entries
}

// augmentList adds overlay entries to a base listing for directory p.
func (o *OverlayFS) augmentList(dir string, items []FileInfo) []FileInfo {
	existing := make(map[string]bool)
	for _, fi := range items {
		existing[fi.Name] = true
	}
	for fp, h := range o.files {
		if path.Dir(fp) != dir {
			continue
		}
		name := path.Base(fp)
		if existing[name] {
			continue
		}
		fi, err := h.Stat()
		if err != nil {
			continue
		}
		items = append(items, FileInfo{
			Name:    name,
			Path:    fp,
			Size:    fi.Size(),
			ModTime: fi.ModTime(),
			IsDir:   fi.IsDir(),
		})
	}
	for d := range o.dirs {
		if path.Dir(d) != dir {
			continue
		}
		name := path.Base(d)
		if existing[name] {
			continue
		}
		existing[name] = true
		items = append(items, FileInfo{
			Name:  name,
			Path:  d,
			IsDir: true,
		})
	}
	for _, m := range o.mounts {
		if path.Dir(m.prefix) != dir {
			continue
		}
		name := path.Base(m.prefix)
		if existing[name] {
			continue
		}
		existing[name] = true
		items = append(items, FileInfo{
			Name:  name,
			Path:  m.prefix,
			IsDir: true,
		})
	}
	return items
}

// augmentDirEntries adds overlay DirEntry entries to a base listing.
func (o *OverlayFS) augmentDirEntries(dir string, entries []fs.DirEntry) []fs.DirEntry {
	existing := make(map[string]bool)
	for _, e := range entries {
		existing[e.Name()] = true
	}
	for fp, h := range o.files {
		if path.Dir(fp) != dir {
			continue
		}
		name := path.Base(fp)
		if existing[name] {
			continue
		}
		fi, err := h.Stat()
		if err != nil {
			continue
		}
		entries = append(entries, &staticDirEntry{info: fi})
	}
	for d := range o.dirs {
		if path.Dir(d) != dir {
			continue
		}
		name := path.Base(d)
		if existing[name] {
			continue
		}
		existing[name] = true
		entries = append(entries, &staticDirEntry{info: &syntheticDirInfo{name: name}})
	}
	for _, m := range o.mounts {
		if path.Dir(m.prefix) != dir {
			continue
		}
		name := path.Base(m.prefix)
		if existing[name] {
			continue
		}
		existing[name] = true
		entries = append(entries, &staticDirEntry{info: &syntheticDirInfo{name: name}})
	}
	return entries
}

// syntheticDirInfo implements fs.FileInfo for implicit virtual directories.
type syntheticDirInfo struct {
	name string
}

func (d *syntheticDirInfo) Name() string       { return d.name }
func (d *syntheticDirInfo) Size() int64        { return 0 }
func (d *syntheticDirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (d *syntheticDirInfo) ModTime() time.Time { return time.Now() }
func (d *syntheticDirInfo) IsDir() bool        { return true }
func (d *syntheticDirInfo) Sys() any           { return nil }

// staticDirEntry wraps an fs.FileInfo as an fs.DirEntry.
type staticDirEntry struct {
	info fs.FileInfo
}

func (e *staticDirEntry) Name() string               { return e.info.Name() }
func (e *staticDirEntry) IsDir() bool                { return e.info.IsDir() }
func (e *staticDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e *staticDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

// vfsFileInfoAdapter wraps vfs.FileInfo as fs.FileInfo (fallback for base without DirReader).
type vfsFileInfoAdapter struct {
	fi *FileInfo
}

func (a *vfsFileInfoAdapter) Name() string       { return a.fi.Name }
func (a *vfsFileInfoAdapter) Size() int64        { return a.fi.Size }
func (a *vfsFileInfoAdapter) ModTime() time.Time { return a.fi.ModTime }
func (a *vfsFileInfoAdapter) IsDir() bool        { return a.fi.IsDir }
func (a *vfsFileInfoAdapter) Sys() any           { return nil }

func (a *vfsFileInfoAdapter) Mode() fs.FileMode {
	if a.fi.IsDir {
		return fs.ModeDir | 0o755
	}
	return 0o644
}

// vfsDirEntryAdapter wraps vfs.FileInfo as fs.DirEntry.
type vfsDirEntryAdapter struct {
	fi *FileInfo
}

func (a *vfsDirEntryAdapter) Name() string               { return a.fi.Name }
func (a *vfsDirEntryAdapter) IsDir() bool                { return a.fi.IsDir }
func (a *vfsDirEntryAdapter) Type() fs.FileMode          { return (&vfsFileInfoAdapter{fi: a.fi}).Mode().Type() }
func (a *vfsDirEntryAdapter) Info() (fs.FileInfo, error) { return &vfsFileInfoAdapter{fi: a.fi}, nil }
