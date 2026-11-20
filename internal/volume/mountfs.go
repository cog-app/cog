package volume

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

// Compile-time interface checks.
var (
	_ vfs.FS         = (*MountFS)(nil)
	_ vfs.Unwrapper  = (*MountFS)(nil)
	_ vfs.FileOpener = (*MountFS)(nil)
	_ vfs.AttrSetter = (*MountFS)(nil)
	_ vfs.DirReader  = (*MountFS)(nil)
)

// MountFS routes VFS operations to backends based on mount path prefixes.
type MountFS struct {
	mounts []mountEntry // sorted longest-prefix first
}

type mountEntry struct {
	userPath      string // e.g. "/" or "/cloud"
	volumeSubpath string
	backend       vfs.FS
	readOnly      bool
	info          MountEntry
}

// NewMountFS creates a MountFS from a set of mount entries.
// Entries are sorted longest-prefix first for correct resolution.
func NewMountFS(entries []MountEntry) *MountFS {
	mounts := make([]mountEntry, len(entries))
	for i, e := range entries {
		mounts[i] = mountEntry{
			userPath:      path.Clean("/" + e.UserPath),
			volumeSubpath: e.VolumeSubpath,
			backend:       e.Backend,
			readOnly:      e.ReadOnly,
			info:          e,
		}
		mounts[i].info.UserPath = mounts[i].userPath
	}
	sort.Slice(mounts, func(i, j int) bool {
		return len(mounts[i].userPath) > len(mounts[j].userPath)
	})
	return &MountFS{mounts: mounts}
}

// MountEntry is the public input type for NewMountFS.
type MountEntry struct {
	UserPath      string
	VolumeSubpath string
	Backend       vfs.FS
	ReadOnly      bool
	// VolumeID is the volume the bytes land on, and ChargeTo the username
	// whose quota on it they count against: the user's own for their
	// mounts, the owner's for a folder shared with them.
	VolumeID string
	ChargeTo string
	// ShareOwner is set on a folder shared with the user: the username who
	// shared it, and ShareOwnerPath where it is in their files.
	ShareOwner     string
	ShareOwnerPath string
	// Reshare says the user may share on what is in a folder shared with
	// them.
	Reshare bool
}

// Mounts returns the mounts, longest path first.
func (m *MountFS) Mounts() []MountEntry {
	out := make([]MountEntry, len(m.mounts))
	for i := range m.mounts {
		out[i] = m.mounts[i].info
	}
	return out
}

// Entry returns the mount holding p as if it were mounted at p: the backend
// and where p is in it, the volume and whose quota, and for a shared folder,
// where p is in its owner's files. A folder in it can be mounted elsewhere
// with what Entry returns, as a folder shared from these files is.
func (m *MountFS) Entry(p string) (MountEntry, error) {
	me, sub, err := m.match(p)
	if err != nil {
		return MountEntry{}, err
	}
	if first, _, _ := strings.Cut(strings.TrimPrefix(sub, "/"), "/"); first == vfs.TrashDir {
		return MountEntry{}, fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	e := me.info
	e.UserPath = path.Clean("/" + p)
	e.VolumeSubpath = path.Join(me.volumeSubpath, sub)
	if e.ShareOwner != "" {
		e.ShareOwnerPath = path.Join(e.ShareOwnerPath, sub)
	}
	return e, nil
}

// ShareTarget reports whether p is in a folder shared with the user, and if
// so whose it is and where p is in their files.
func (m *MountFS) ShareTarget(p string) (owner, ownerPath string, ok bool) {
	me, sub, err := m.match(p)
	if err != nil || me.info.ShareOwner == "" {
		return "", "", false
	}
	return me.info.ShareOwner, path.Join(me.info.ShareOwnerPath, sub), true
}

// resolve finds the mount matching the given path and returns the backend,
// the subpath within the backend, and whether the mount is read-only.
func (m *MountFS) resolve(p string) (vfs.FS, string, bool, error) {
	me, sub, err := m.match(p)
	if err != nil {
		return nil, "", false, err
	}
	// The trash at a mount's root is reached only through MountRoot.
	if first, _, _ := strings.Cut(strings.TrimPrefix(sub, "/"), "/"); first == vfs.TrashDir {
		return nil, "", false, fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	return me.backend, path.Join(me.volumeSubpath, sub), me.readOnly, nil
}

// match finds the mount holding p, and p's path within the mount.
func (m *MountFS) match(p string) (*mountEntry, string, error) {
	p = path.Clean("/" + p)
	for i := range m.mounts {
		me := &m.mounts[i]
		switch {
		case p == me.userPath:
			return me, "/", nil
		case me.userPath == "/":
			// Root mount matches all paths; sub retains the leading slash.
			return me, p, nil
		case strings.HasPrefix(p, me.userPath+"/"):
			return me, strings.TrimPrefix(p, me.userPath), nil
		}
	}
	return nil, "", fmt.Errorf("no mount for path: %s", p)
}

// atMountRoot reports whether p is the root of a mount, whose listing hides
// the trash.
// ReadOnly reports whether p is on a mount that may not be written, so a
// listing can say so before a client tries.
func (m *MountFS) ReadOnly(p string) bool {
	me, _, err := m.match(p)
	return err == nil && me.readOnly
}

func (m *MountFS) atMountRoot(p string) bool {
	_, sub, err := m.match(p)
	return err == nil && sub == "/"
}

// MountRoot implements vfs.MountRooter.
func (m *MountFS) MountRoot(p string) (vfs.FS, string, string, bool, error) {
	me, _, err := m.match(p)
	if err != nil {
		return nil, "", "", false, err
	}
	return me.backend, path.Join("/", me.volumeSubpath), me.userPath, me.readOnly, nil
}

// Unwrap resolves a path to the underlying FS and the subpath within it.
func (m *MountFS) Unwrap(p string) (vfs.FS, string, error) {
	backend, subpath, _, err := m.resolve(p)
	return backend, subpath, err
}

func (m *MountFS) checkWrite(p string) (vfs.FS, string, error) {
	backend, subpath, ro, err := m.resolve(p)
	if err != nil {
		return nil, "", err
	}
	if ro {
		return nil, "", vfs.ErrReadOnly
	}
	return backend, subpath, nil
}

// List returns entries in a directory, merging mount points that appear
// within the listed directory as synthetic entries.
func (m *MountFS) List(p string) ([]vfs.FileInfo, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}

	items, err := backend.List(subpath)
	if err != nil {
		return nil, err
	}
	if m.atMountRoot(p) {
		items = slices.DeleteFunc(items, func(fi vfs.FileInfo) bool { return fi.Name == vfs.TrashDir })
	}

	// Rewrite paths from backend-relative to user-relative.
	cleanP := path.Clean("/" + p)
	for i := range items {
		items[i].Path = path.Join(cleanP, items[i].Name)
	}

	// Merge synthetic entries for mount points that are direct children.
	existing := make(map[string]bool)
	for _, fi := range items {
		existing[fi.Name] = true
	}
	for _, me := range m.mounts {
		if me.userPath == "/" {
			continue
		}
		parent := path.Dir(me.userPath)
		if parent != cleanP {
			continue
		}
		name := path.Base(me.userPath)
		if existing[name] {
			continue
		}
		items = append(items, vfs.FileInfo{
			Name:  name,
			Path:  me.userPath,
			IsDir: true,
		})
	}

	return items, nil
}

func (m *MountFS) Stat(p string) (*vfs.FileInfo, error) {
	// Check if the path matches a mount point exactly — even if the backend
	// path doesn't exist yet, the mount point itself is a valid directory.
	cleanP := path.Clean("/" + p)
	for _, me := range m.mounts {
		if me.userPath == cleanP {
			fi, err := me.backend.Stat(me.volumeSubpath)
			if err != nil {
				// Mount point exists but backend path may not.
				return &vfs.FileInfo{
					Name:  path.Base(cleanP),
					Path:  cleanP,
					IsDir: true,
				}, nil
			}
			fi.Path = cleanP
			fi.Name = path.Base(cleanP)
			return fi, nil
		}
	}

	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	fi, err := backend.Stat(subpath)
	if err != nil {
		return nil, err
	}
	fi.Path = cleanP
	return fi, nil
}

func (m *MountFS) Get(p string) (io.ReadCloser, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	return backend.Get(subpath)
}

func (m *MountFS) Put(p string, content io.Reader) error {
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	return backend.Put(subpath, content)
}

func (m *MountFS) Mkdir(p string) error {
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	return backend.Mkdir(subpath)
}

// isMountPoint reports whether p is where a mount other than the root is:
// removing or moving it would remove or move what is mounted, which is
// unmounting's to do.
func (m *MountFS) isMountPoint(p string) bool {
	p = path.Clean("/" + p)
	for _, me := range m.mounts {
		if me.userPath == p && p != "/" {
			return true
		}
	}
	return false
}

// ErrMountPoint refuses removing or moving a mount point.
var ErrMountPoint = fmt.Errorf("a mounted folder is removed by unmounting it: %w", fs.ErrPermission)

func (m *MountFS) Delete(p string) error {
	if m.isMountPoint(p) {
		return ErrMountPoint
	}
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	return backend.Delete(subpath)
}

func (m *MountFS) Rename(oldPath, newPath string) error {
	if m.isMountPoint(oldPath) || m.isMountPoint(newPath) {
		return ErrMountPoint
	}
	oldBackend, oldSub, oldRO, err := m.resolve(oldPath)
	if err != nil {
		return err
	}
	newBackend, newSub, newRO, err := m.resolve(newPath)
	if err != nil {
		return err
	}
	if oldBackend != newBackend {
		return vfs.ErrCrossMount
	}
	if oldRO || newRO {
		return vfs.ErrReadOnly
	}
	return oldBackend.Rename(oldSub, newSub)
}

func (m *MountFS) ReadFile(p string) ([]byte, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	return backend.ReadFile(subpath)
}

// --- vfs.FileOpener ---

func (m *MountFS) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	backend, subpath, ro, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	if ro && (flag&(os.O_WRONLY|os.O_RDWR|os.O_CREATE|os.O_TRUNC|os.O_APPEND)) != 0 {
		return nil, vfs.ErrReadOnly
	}
	if opener, ok := backend.(vfs.FileOpener); ok {
		return opener.OpenFile(subpath, flag, perm)
	}
	return nil, fmt.Errorf("backend does not support OpenFile")
}

// --- vfs.AttrSetter ---

func (m *MountFS) Chmod(p string, mode os.FileMode) error {
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	if setter, ok := backend.(vfs.AttrSetter); ok {
		return setter.Chmod(subpath, mode)
	}
	return fmt.Errorf("backend does not support Chmod")
}

func (m *MountFS) Chtimes(p string, atime, mtime time.Time) error {
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	if setter, ok := backend.(vfs.AttrSetter); ok {
		return setter.Chtimes(subpath, atime, mtime)
	}
	return fmt.Errorf("backend does not support Chtimes")
}

func (m *MountFS) Truncate(p string, size int64) error {
	backend, subpath, err := m.checkWrite(p)
	if err != nil {
		return err
	}
	if setter, ok := backend.(vfs.AttrSetter); ok {
		return setter.Truncate(subpath, size)
	}
	return fmt.Errorf("backend does not support Truncate")
}

func (m *MountFS) Link(oldname, newname string) error {
	oldBackend, oldSub, oldRO, err := m.resolve(oldname)
	if err != nil {
		return err
	}
	newBackend, newSub, newRO, err := m.resolve(newname)
	if err != nil {
		return err
	}
	if oldBackend != newBackend {
		return vfs.ErrCrossMount
	}
	if oldRO || newRO {
		return vfs.ErrReadOnly
	}
	if setter, ok := oldBackend.(vfs.AttrSetter); ok {
		return setter.Link(oldSub, newSub)
	}
	return fmt.Errorf("backend does not support Link")
}

// --- vfs.DirReader ---

func (m *MountFS) StatInfo(p string) (fs.FileInfo, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	if dr, ok := backend.(vfs.DirReader); ok {
		return dr.StatInfo(subpath)
	}
	// Fallback: use Stat and synthesize an fs.FileInfo.
	fi, err := backend.Stat(subpath)
	if err != nil {
		return nil, err
	}
	return &syntheticFileInfo{fi: fi}, nil
}

func (m *MountFS) LstatInfo(p string) (fs.FileInfo, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	if dr, ok := backend.(vfs.DirReader); ok {
		return dr.LstatInfo(subpath)
	}
	fi, err := backend.Stat(subpath)
	if err != nil {
		return nil, err
	}
	return &syntheticFileInfo{fi: fi}, nil
}

func (m *MountFS) ReadDirEntries(p string) ([]fs.DirEntry, error) {
	backend, subpath, _, err := m.resolve(p)
	if err != nil {
		return nil, err
	}
	if dr, ok := backend.(vfs.DirReader); ok {
		entries, err := dr.ReadDirEntries(subpath)
		if err != nil {
			return nil, err
		}
		// Merge mount points.
		return m.mergeDirEntries(p, entries), nil
	}
	// Fallback: use List and synthesize DirEntry.
	items, err := backend.List(subpath)
	if err != nil {
		return nil, err
	}
	entries := make([]fs.DirEntry, len(items))
	for i := range items {
		entries[i] = &syntheticDirEntry{fi: &items[i]}
	}
	return m.mergeDirEntries(p, entries), nil
}

// mergeDirEntries adds synthetic entries for mount points within p.
func (m *MountFS) mergeDirEntries(p string, entries []fs.DirEntry) []fs.DirEntry {
	if m.atMountRoot(p) {
		entries = slices.DeleteFunc(entries, func(e fs.DirEntry) bool { return e.Name() == vfs.TrashDir })
	}
	cleanP := path.Clean("/" + p)
	existing := make(map[string]bool)
	for _, e := range entries {
		existing[e.Name()] = true
	}
	for _, me := range m.mounts {
		if me.userPath == "/" {
			continue
		}
		parent := path.Dir(me.userPath)
		if parent != cleanP {
			continue
		}
		name := path.Base(me.userPath)
		if existing[name] {
			continue
		}
		entries = append(entries, &syntheticDirEntry{
			fi: &vfs.FileInfo{Name: name, Path: me.userPath, IsDir: true},
		})
	}
	return entries
}

// syntheticFileInfo wraps vfs.FileInfo to implement fs.FileInfo.
type syntheticFileInfo struct {
	fi *vfs.FileInfo
}

func (s *syntheticFileInfo) Name() string       { return s.fi.Name }
func (s *syntheticFileInfo) Size() int64        { return s.fi.Size }
func (s *syntheticFileInfo) ModTime() time.Time { return s.fi.ModTime }
func (s *syntheticFileInfo) IsDir() bool        { return s.fi.IsDir }
func (s *syntheticFileInfo) Sys() any           { return nil }

func (s *syntheticFileInfo) Mode() fs.FileMode {
	if s.fi.IsDir {
		return fs.ModeDir | 0755
	}
	return 0644
}

// syntheticDirEntry wraps vfs.FileInfo to implement fs.DirEntry.
type syntheticDirEntry struct {
	fi *vfs.FileInfo
}

func (s *syntheticDirEntry) Name() string               { return s.fi.Name }
func (s *syntheticDirEntry) IsDir() bool                { return s.fi.IsDir }
func (s *syntheticDirEntry) Type() fs.FileMode          { return (&syntheticFileInfo{fi: s.fi}).Mode().Type() }
func (s *syntheticDirEntry) Info() (fs.FileInfo, error) { return &syntheticFileInfo{fi: s.fi}, nil }
