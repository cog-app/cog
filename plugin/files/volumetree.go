package files

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// VolumeView is what of a volume a user may look at.
type VolumeView struct {
	Volume *volume.Volume
	// Root is where the view starts in the volume: "" for the whole of a
	// volume they may see whole, their own directory of areas on a storage
	// volume shared with them.
	Root string
}

// Areas reports whether the view is of the user's areas, not the volume.
func (v *VolumeView) Areas() bool { return v.Root != "" }

// Views returns the volumes u may look at, by name, and what of each.
func (s *Shared) Views(u *user.User) ([]VolumeView, error) {
	var views []VolumeView
	for vol, err := range s.Access.ListAccessible(u.ID) {
		if err != nil {
			return nil, err
		}
		v, ok, err := s.view(u, &vol)
		if err != nil {
			return nil, err
		}
		if ok {
			views = append(views, v)
		}
	}
	slices.SortFunc(views, func(a, b VolumeView) int { return strings.Compare(a.Volume.Name, b.Volume.Name) })
	return views, nil
}

func (s *Shared) view(u *user.User, vol *volume.Volume) (VolumeView, bool, error) {
	perms, err := s.Access.WholePermissions(u.ID, vol)
	if err != nil {
		return VolumeView{}, false, err
	}
	if perms&plugin.PermRead != 0 {
		return VolumeView{Volume: vol}, true, nil
	}
	ok, err := s.Access.MayMakeAreas(u.ID, vol)
	if err != nil || !ok {
		return VolumeView{}, false, err
	}
	return VolumeView{Volume: vol, Root: u.Username}, true, nil
}

// VolumeTree returns the volumes u may look at as a filesystem, for SSH's
// /volumes: a directory per volume, holding the volume, or on a storage
// volume shared with them, their own areas. It is read-only: what is written
// to a volume goes through a mount of it, which keeps the files' records and
// holds them to the quota. The server's own files are not in it.
func (s *Shared) VolumeTree(u *user.User) vfs.FS {
	return &volumeTree{s: s, u: u}
}

type volumeTree struct {
	s *Shared
	u *user.User
}

var _ vfs.FileOpener = (*volumeTree)(nil)

// find returns the view p is in, and p's path in the volume; no view for
// the root.
func (t *volumeTree) find(p string) (*VolumeView, string, error) {
	p = path.Clean("/" + p)
	if p == "/" {
		return nil, "", nil
	}
	name, rest, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
	vol, err := t.s.Volumes.GetByName(name)
	if errors.Is(err, volume.ErrNotFound) {
		return nil, "", fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	if err != nil {
		return nil, "", err
	}
	v, ok, err := t.s.view(t.u, vol)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	at := path.Join("/", v.Root, rest)
	if t.s.Server.Internal(vol, at) || inTrash(at) {
		return nil, "", fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
	}
	return &v, at, nil
}

// inTrash reports whether p is in a mount's trash, which only the files
// plugin reaches.
func inTrash(p string) bool {
	return slices.Contains(strings.Split(p, "/"), vfs.TrashDir)
}

func (t *volumeTree) backend(v *VolumeView) (vfs.FS, error) {
	return volume.NewBackend(v.Volume)
}

func (t *volumeTree) List(p string) ([]vfs.FileInfo, error) {
	v, at, err := t.find(p)
	if err != nil {
		return nil, err
	}
	p = path.Clean("/" + p)
	if v == nil {
		views, err := t.s.Views(t.u)
		if err != nil {
			return nil, err
		}
		items := make([]vfs.FileInfo, len(views))
		for i, v := range views {
			items[i] = vfs.FileInfo{Name: v.Volume.Name, Path: "/" + v.Volume.Name, IsDir: true}
		}
		return items, nil
	}
	b, err := t.backend(v)
	if err != nil {
		return nil, err
	}
	items, err := b.List(at)
	if err != nil {
		if v.Areas() && at == "/"+v.Root && (errors.Is(err, vfs.ErrNotFound) || errors.Is(err, iofs.ErrNotExist)) {
			return []vfs.FileInfo{}, nil // no areas yet
		}
		return nil, err
	}
	items = slices.DeleteFunc(items, func(fi vfs.FileInfo) bool {
		child := path.Join(at, fi.Name)
		return t.s.Server.Internal(v.Volume, child) || inTrash(child) || vfs.IsPartial(fi.Name)
	})
	for i := range items {
		items[i].Path = path.Join(p, items[i].Name)
	}
	return items, nil
}

func (t *volumeTree) Stat(p string) (*vfs.FileInfo, error) {
	v, at, err := t.find(p)
	if err != nil {
		return nil, err
	}
	p = path.Clean("/" + p)
	if v == nil || at == "/"+v.Root || at == "/" {
		return &vfs.FileInfo{Name: path.Base(p), Path: p, IsDir: true}, nil
	}
	b, err := t.backend(v)
	if err != nil {
		return nil, err
	}
	fi, err := b.Stat(at)
	if err != nil {
		return nil, err
	}
	fi.Path, fi.Name = p, path.Base(p)
	return fi, nil
}

func (t *volumeTree) file(p string) (vfs.FS, string, error) {
	v, at, err := t.find(p)
	if err != nil {
		return nil, "", err
	}
	if v == nil {
		return nil, "", fmt.Errorf("%s: is a directory", p)
	}
	b, err := t.backend(v)
	return b, at, err
}

func (t *volumeTree) Get(p string) (io.ReadCloser, error) {
	b, at, err := t.file(p)
	if err != nil {
		return nil, err
	}
	return b.Get(at)
}

func (t *volumeTree) ReadFile(p string) ([]byte, error) {
	b, at, err := t.file(p)
	if err != nil {
		return nil, err
	}
	return b.ReadFile(at)
}

func (t *volumeTree) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, t.readOnly(p)
	}
	b, at, err := t.file(p)
	if err != nil {
		return nil, err
	}
	opener, ok := b.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("open %s: %w", p, errors.ErrUnsupported)
	}
	return opener.OpenFile(at, flag, perm)
}

// readOnly refuses a change, saying how to make one.
func (t *volumeTree) readOnly(p string) error {
	return fmt.Errorf("%s: /volumes is for looking; ln -s it into /files to change it: %w", p, vfs.ErrReadOnly)
}

func (t *volumeTree) Put(p string, _ io.Reader) error { return t.readOnly(p) }
func (t *volumeTree) Mkdir(p string) error            { return t.readOnly(p) }
func (t *volumeTree) Delete(p string) error           { return t.readOnly(p) }
func (t *volumeTree) Rename(p, _ string) error        { return t.readOnly(p) }

// RenameNoReplace is refused like Rename, rather than as unsupported.
func (t *volumeTree) RenameNoReplace(p, _ string) error { return t.readOnly(p) }

// VolumeTarget is the volume and the path in it that p, a path of the tree,
// names, for a mount of it.
func (s *Shared) VolumeTarget(u *user.User, p string) (*volume.Volume, string, error) {
	t := &volumeTree{s: s, u: u}
	v, at, err := t.find(p)
	if err != nil {
		return nil, "", err
	}
	if v == nil {
		return nil, "", fmt.Errorf("%w: name a volume, /volumes/<name>", ErrMountPoint)
	}
	// MountVolume takes a storage view's path from the areas' directory.
	rel := strings.TrimPrefix(at, "/"+v.Root)
	if !v.Areas() {
		rel = at
		// An area that does not exist is made by mounting it; a folder of a
		// volume is not, so a mount of one that is not there shows nothing.
		fi, err := t.Stat(p)
		if errors.Is(err, vfs.ErrNotFound) || errors.Is(err, iofs.ErrNotExist) {
			return nil, "", fmt.Errorf("no such folder: %w", vfs.ErrNotFound)
		}
		if err != nil {
			return nil, "", err
		}
		if !fi.IsDir {
			return nil, "", fmt.Errorf("%w: not a folder", ErrMountPoint)
		}
	}
	return v.Volume, strings.Trim(rel, "/"), nil
}
