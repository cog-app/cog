package files

import (
	"errors"
	"path/filepath"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/vfs"
)

// A folder someone shared with a user can be mounted in the user's files.
// Its bytes are the owner's, and so are its rows: the owner's files keep the
// IDs the shares name, their trash takes what is deleted, and their quota
// counts what is written. So a change made through the mount is made as the
// owner, in the owner's files, and the user's own rows follow it; one made
// beneath the plugin (SSH writes straight to the mount) is recorded on both
// sides.

// errMountPoint refuses removing or moving a mount point: that is
// unmounting, which is done to the mount, not to the files behind it.
var errMountPoint = volume.ErrMountPoint

// sharedTarget is where path, in a folder shared with username, is in its
// owner's files.
type sharedTarget struct {
	owner     string
	path      string
	readOnly  bool // the share does not let username write
	mountRoot bool // path is the mount point itself
}

// shared reports whether path is in a folder shared with username and
// mounted in their files, and where it is in its owner's.
func (p *Plugin) shared(username, path string) (sharedTarget, bool, error) {
	fsys, err := p.userFS(username)
	if err != nil {
		return sharedTarget{}, false, err
	}
	st, ok := fsys.(interface {
		ShareTarget(string) (string, string, bool)
	})
	if !ok {
		return sharedTarget{}, false, nil
	}
	owner, ownerPath, ok := st.ShareTarget(path)
	if !ok {
		return sharedTarget{}, false, nil
	}
	t := sharedTarget{owner: owner, path: ownerPath}
	if ro, ok := fsys.(interface{ ReadOnly(string) bool }); ok {
		t.readOnly = ro.ReadOnly(path)
	}
	if mr, ok := fsys.(vfs.MountRooter); ok {
		if _, _, mountPath, _, err := mr.MountRoot(path); err == nil {
			t.mountRoot = mountPath == normalizePath(path)
		}
	}
	return t, true, nil
}

// writable returns the owner's side of path for a change to it, or an error
// when the share does not allow one.
func (p *Plugin) writable(username, path string) (sharedTarget, bool, error) {
	t, ok, err := p.shared(username, path)
	if err != nil || !ok {
		return t, ok, err
	}
	if t.readOnly {
		return t, true, vfs.ErrReadOnly
	}
	return t, true, nil
}

// recordShared records path on its owner's side after it changed beneath
// the plugin through username's mount of a folder shared with them, and on
// theirs in turn if they too had it from someone.
func (p *Plugin) recordShared(username, path string) error {
	t, ok, err := p.shared(username, path)
	if err != nil || !ok {
		return err
	}
	db, err := p.DB(t.owner)
	if err != nil {
		return err
	}
	fsys, err := p.userFS(t.owner)
	if err != nil {
		return err
	}
	if _, err := p.record(db, fsys, t.path); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if err := p.recordDirs(db, fsys, filepath.Dir(t.path)); err != nil {
		return err
	}
	return p.recordShared(t.owner, t.path)
}

// follow brings username's rows of path in step after the owner's side
// changed: path, and the folders above it, recorded or forgotten.
func (p *Plugin) follow(username, path string) (*FileMeta, error) {
	db, err := p.DB(username)
	if err != nil {
		return nil, err
	}
	fsys, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	if err := p.recordDirs(db, fsys, filepath.Dir(path)); err != nil {
		return nil, err
	}
	return p.record(db, fsys, path)
}

// DirPath returns where the directory id is in owner's files, or "" when it
// is not there: what a mount of a folder they shared follows.
func (p *Plugin) DirPath(owner, id string) (string, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", nil // names no file
	}
	meta, err := p.GetByID(owner, n)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !meta.IsDir {
		return "", nil
	}
	return meta.Path, nil
}

// treeChanged says a folder moved or went, so mounts of folders shared from
// it are found again.
func (p *Plugin) treeChanged() {
	if p.priv != nil {
		p.priv.TreeChanged()
	}
}

// Placement is where a path of a user's files is, for a client that shows
// who owns what and what may be done with it.
type Placement struct {
	ReadOnly bool
	// Owner is the username a folder shared with the user, and so the path,
	// belongs to; empty for the user's own.
	Owner string
	// MountPoint is set on the mount point itself, which is unmounted
	// rather than deleted, renamed or moved.
	MountPoint bool
	// Reshare says what Owner shared may be shared on.
	Reshare bool
}

// Placements returns what reports each path's Placement, built once per
// listing: the tree is resolved once and the checks touch no disk.
func (p *Plugin) Placements(username string) (func(path string) Placement, error) {
	fsys, err := p.userFS(username)
	if err != nil {
		return nil, err
	}
	ro, _ := fsys.(interface{ ReadOnly(string) bool })
	st, _ := fsys.(interface {
		ShareTarget(string) (string, string, bool)
	})
	mr, _ := fsys.(vfs.MountRooter)
	entry, _ := fsys.(interface {
		Entry(string) (volume.MountEntry, error)
	})
	return func(path string) Placement {
		path = normalizePath(path)
		var pl Placement
		if ro != nil {
			pl.ReadOnly = ro.ReadOnly(path)
		}
		if st != nil {
			if owner, _, ok := st.ShareTarget(path); ok {
				pl.Owner = owner
				if entry != nil {
					if e, err := entry.Entry(path); err == nil {
						pl.Reshare = e.Reshare
					}
				}
			}
		}
		if mr != nil && path != "/" {
			if _, _, mountPath, _, err := mr.MountRoot(path); err == nil {
				pl.MountPoint = mountPath == path
			}
		}
		return pl
	}, nil
}

// ErrNoReshare refuses sharing what is in a folder shared without the
// permission to share it on.
var ErrNoReshare = errors.New("this was shared with you without permission to share it")

// MayShare reports whether username may share the file at filePath: their
// own, or in a folder shared with them that they may share on.
func (p *Plugin) MayShare(username, filePath string) error {
	place, err := p.Placements(username)
	if err != nil {
		return err
	}
	if pl := place(filePath); pl.Owner != "" && !pl.Reshare {
		return ErrNoReshare
	}
	return nil
}
