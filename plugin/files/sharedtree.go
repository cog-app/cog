package files

import (
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Tree returns what was shared with u as a filesystem, for SSH's /shared:
// a directory per owner, in it a directory per shared folder or volume and
// a file per shared file. A shared folder is the owner's files beneath it,
// writable as the share allows; a volume is empty, being somewhere to keep
// storage rather than something to look in.
func (s *Shared) Tree(u *user.User) vfs.FS {
	return &sharedTree{s: s, u: u}
}

// treeTTL is how long a tree trusts what it last read of the shares: a
// transfer makes many calls in a row, and each would read them again.
const treeTTL = 5 * time.Second

type sharedTree struct {
	s *Shared
	u *user.User

	mu      sync.Mutex
	owners  []SharedOwner
	expires time.Time
}

var (
	_ vfs.FileOpener = (*sharedTree)(nil)
	_ vfs.AttrSetter = (*sharedTree)(nil)
	_ vfs.Replacer   = (*sharedTree)(nil)
)

func (t *sharedTree) list() ([]SharedOwner, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now := time.Now(); now.Before(t.expires) {
		return t.owners, nil
	}
	owners, err := t.s.List(t.u)
	if err != nil {
		return nil, err
	}
	t.owners, t.expires = owners, time.Now().Add(treeTTL)
	return owners, nil
}

// node is what a path of the tree is: the root (no owner), an owner's
// directory (no entry), or in an entry, with rel the path inside it.
type node struct {
	owner *SharedOwner
	entry *SharedEntry
	rel   string
}

func (t *sharedTree) find(p string) (node, error) {
	p = path.Clean("/" + p)
	if p == "/" {
		return node{}, nil
	}
	parts := strings.SplitN(strings.TrimPrefix(p, "/"), "/", 3)
	owners, err := t.list()
	if err != nil {
		return node{}, err
	}
	for i := range owners {
		o := &owners[i]
		if o.User.Username != parts[0] {
			continue
		}
		if len(parts) == 1 {
			return node{owner: o}, nil
		}
		for j := range o.Entries {
			e := &o.Entries[j]
			if e.Name != parts[1] {
				continue
			}
			rel := "/"
			if len(parts) == 3 {
				rel = "/" + parts[2]
			}
			if e.Kind != SharedFolder && rel != "/" {
				break
			}
			return node{owner: o, entry: e, rel: rel}, nil
		}
	}
	return node{}, fmt.Errorf("%s: %w", p, vfs.ErrNotFound)
}

// folder returns the filesystem of the shared folder n is in, and n's path
// in it; an error for anything else.
func (t *sharedTree) folder(n node, p string) (vfs.FS, string, error) {
	if n.entry == nil || n.entry.Kind != SharedFolder {
		return nil, "", fmt.Errorf("%s: %w", p, iofs.ErrPermission)
	}
	fsys, err := t.s.FS(n.entry)
	return fsys, n.rel, err
}

func dirInfo(name, p string) vfs.FileInfo {
	return vfs.FileInfo{Name: name, Path: p, IsDir: true}
}

func (t *sharedTree) List(p string) ([]vfs.FileInfo, error) {
	n, err := t.find(p)
	if err != nil {
		return nil, err
	}
	p = path.Clean("/" + p)
	switch {
	case n.owner == nil:
		owners, err := t.list()
		if err != nil {
			return nil, err
		}
		items := make([]vfs.FileInfo, len(owners))
		for i, o := range owners {
			items[i] = dirInfo(o.User.Username, "/"+o.User.Username)
		}
		return items, nil
	case n.entry == nil:
		var items []vfs.FileInfo
		for _, e := range n.owner.Entries {
			fi := dirInfo(e.Name, path.Join(p, e.Name))
			if e.Kind == SharedFile {
				fi.IsDir = false
				fi.Size = e.File.Size
				fi.ModTime = time.Unix(e.File.MTime, 0)
				fi.MimeType = e.File.MimeType
			}
			items = append(items, fi)
		}
		return items, nil
	case n.entry.Kind == SharedVolume:
		return []vfs.FileInfo{}, nil
	case n.entry.Kind == SharedFile:
		return nil, fmt.Errorf("%s: not a directory", p)
	}
	fsys, rel, err := t.folder(n, p)
	if err != nil {
		return nil, err
	}
	items, err := fsys.List(rel)
	for i := range items {
		items[i].Path = path.Join(p, items[i].Name)
	}
	return items, err
}

func (t *sharedTree) Stat(p string) (*vfs.FileInfo, error) {
	n, err := t.find(p)
	if err != nil {
		return nil, err
	}
	p = path.Clean("/" + p)
	switch {
	case n.owner == nil:
		fi := dirInfo("/", "/")
		return &fi, nil
	case n.entry == nil:
		fi := dirInfo(n.owner.User.Username, p)
		return &fi, nil
	case n.entry.Kind == SharedVolume:
		fi := dirInfo(n.entry.Name, p)
		return &fi, nil
	case n.entry.Kind == SharedFile:
		return &vfs.FileInfo{Name: n.entry.Name, Path: p, Size: n.entry.File.Size,
			ModTime: time.Unix(n.entry.File.MTime, 0), MimeType: n.entry.File.MimeType}, nil
	}
	fsys, rel, err := t.folder(n, p)
	if err != nil {
		return nil, err
	}
	fi, err := fsys.Stat(rel)
	if err != nil {
		return nil, err
	}
	fi.Path = p
	fi.Name = path.Base(p)
	return fi, nil
}

// fileFS returns the filesystem holding the file at p and its path there:
// a shared file is read from its owner's files.
func (t *sharedTree) fileFS(p string) (vfs.FS, string, error) {
	n, err := t.find(p)
	if err != nil {
		return nil, "", err
	}
	if n.entry != nil && n.entry.Kind == SharedFile {
		fsys, err := t.s.Files.FS(n.entry.Owner.Username)
		if err != nil {
			return nil, "", err
		}
		return vfs.Sub(fsys, n.entry.Path, true), "/", nil
	}
	return t.folder(n, p)
}

func (t *sharedTree) Get(p string) (io.ReadCloser, error) {
	fsys, rel, err := t.fileFS(p)
	if err != nil {
		return nil, err
	}
	return fsys.Get(rel)
}

func (t *sharedTree) ReadFile(p string) ([]byte, error) {
	fsys, rel, err := t.fileFS(p)
	if err != nil {
		return nil, err
	}
	return fsys.ReadFile(rel)
}

// within returns the filesystem of the shared folder p is inside, for a
// change; the tree around the folders cannot be changed.
func (t *sharedTree) within(p string) (vfs.FS, string, error) {
	n, err := t.find(p)
	if errors.Is(err, vfs.ErrNotFound) {
		// Something new in a folder: find the folder by its parent.
		parent, perr := t.find(path.Dir(path.Clean("/" + p)))
		if perr != nil || parent.entry == nil {
			return nil, "", err
		}
		fsys, rel, ferr := t.folder(parent, p)
		if ferr != nil {
			return nil, "", ferr
		}
		return fsys, path.Join(rel, path.Base(p)), nil
	}
	if err != nil {
		return nil, "", err
	}
	if n.entry == nil || n.rel == "/" {
		return nil, "", fmt.Errorf("%s: %w", p, iofs.ErrPermission)
	}
	return t.folder(n, p)
}

func (t *sharedTree) Put(p string, content io.Reader) error {
	fsys, rel, err := t.within(p)
	if err != nil {
		return err
	}
	return fsys.Put(rel, content)
}

func (t *sharedTree) Mkdir(p string) error {
	if n, err := t.find(p); err == nil && (n.entry == nil || n.entry.Kind != SharedFolder || n.rel == "/") {
		return nil // already a directory of the tree
	}
	fsys, rel, err := t.within(p)
	if err != nil {
		return err
	}
	return fsys.Mkdir(rel)
}

func (t *sharedTree) Delete(p string) error {
	fsys, rel, err := t.within(p)
	if err != nil {
		return err
	}
	return fsys.Delete(rel)
}

// pair resolves both ends of a move, which must be in one shared folder.
func (t *sharedTree) pair(oldPath, newPath string) (vfs.FS, string, string, error) {
	from, fromRel, err := t.within(oldPath)
	if err != nil {
		return nil, "", "", err
	}
	_, toRel, err := t.within(newPath)
	if err != nil {
		return nil, "", "", err
	}
	a, _ := t.find(oldPath)
	b, _ := t.find(path.Dir(path.Clean("/" + newPath)))
	if a.entry == nil || b.entry == nil || a.entry.Owner.ID != b.entry.Owner.ID || a.entry.ResourceID != b.entry.ResourceID {
		return nil, "", "", vfs.ErrCrossMount
	}
	return from, fromRel, toRel, nil
}

func (t *sharedTree) Rename(oldPath, newPath string) error {
	fsys, from, to, err := t.pair(oldPath, newPath)
	if err != nil {
		return err
	}
	return fsys.Rename(from, to)
}

func (t *sharedTree) RenameNoReplace(oldPath, newPath string) error {
	fsys, from, to, err := t.pair(oldPath, newPath)
	if err != nil {
		return err
	}
	return vfs.RenameNoReplace(fsys, from, to)
}

func (t *sharedTree) Replace(oldPath, newPath string) error {
	fsys, from, to, err := t.pair(oldPath, newPath)
	if err != nil {
		return err
	}
	return vfs.Replace(fsys, from, to)
}

func (t *sharedTree) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	var fsys vfs.FS
	var rel string
	var err error
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) == 0 {
		fsys, rel, err = t.fileFS(p)
	} else {
		fsys, rel, err = t.within(p)
	}
	if err != nil {
		return nil, err
	}
	opener, ok := fsys.(vfs.FileOpener)
	if !ok {
		return nil, fmt.Errorf("open %s: %w", p, errors.ErrUnsupported)
	}
	return opener.OpenFile(rel, flag, perm)
}

func (t *sharedTree) attrSetter(p string) (vfs.AttrSetter, string, error) {
	fsys, rel, err := t.within(p)
	if err != nil {
		return nil, "", err
	}
	as, ok := fsys.(vfs.AttrSetter)
	if !ok {
		return nil, "", fmt.Errorf("set attributes of %s: %w", p, errors.ErrUnsupported)
	}
	return as, rel, nil
}

func (t *sharedTree) Chmod(p string, mode os.FileMode) error {
	as, rel, err := t.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Chmod(rel, mode)
}

func (t *sharedTree) Chtimes(p string, atime, mtime time.Time) error {
	as, rel, err := t.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Chtimes(rel, atime, mtime)
}

func (t *sharedTree) Truncate(p string, size int64) error {
	as, rel, err := t.attrSetter(p)
	if err != nil {
		return err
	}
	return as.Truncate(rel, size)
}

func (t *sharedTree) Link(oldname, newname string) error {
	return fmt.Errorf("link %s: %w", newname, errors.ErrUnsupported)
}
