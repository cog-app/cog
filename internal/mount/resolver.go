package mount

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Resolver bridges the mount system to the plugin registry.
//
// A user's VFS is built from their mounts, each mount's volume and what a
// share lets them do with it, and every file operation needs it, so it is
// kept once built. The mount and volume stores, and the share store through
// services, say when any of that changes; cacheTTL bounds what they might
// miss.
type Resolver struct {
	users   *user.Store
	mounts  *Store
	volumes *volume.Store
	access  *volume.Access

	// DirPath returns where the directory resourceID is in owner's files,
	// or "" when it is not there any more: a shared folder is named by its
	// ID, and its path changes when it is moved.
	DirPath func(owner, resourceID string) (string, error)

	mu    sync.Mutex
	cache map[string]cachedFS // by username
}

type cachedFS struct {
	fs      vfs.FS
	expires time.Time
}

// cacheTTL is how long a user's VFS is trusted without a change notice.
const cacheTTL = 30 * time.Second

// SharedDirType is the share resource type of a directory of the files
// plugin: what a share mount mounts.
const SharedDirType = "files:directory"

// maxShareDepth bounds how far a share mount is followed through the
// owner's own share mounts (a folder reshared), so that two users who mount
// each other's folders end rather than build each other's trees forever.
const maxShareDepth = 3

// NewResolver creates a new mount resolver. Access decides what a mount of a
// volume other than the user's root may do: a mount outlives the share that
// allowed it, so it is checked each time it is used.
func NewResolver(users *user.Store, mounts *Store, volumes *volume.Store, access *volume.Access) *Resolver {
	r := &Resolver{users: users, mounts: mounts, volumes: volumes, access: access, cache: map[string]cachedFS{}}
	mounts.Changed = append(mounts.Changed, r.InvalidateAll)
	volumes.Changed = append(volumes.Changed, r.InvalidateAll)
	return r
}

// InvalidateAll forgets every cached VFS: something a mount depends on
// changed, and the next use rebuilds it.
func (r *Resolver) InvalidateAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	clear(r.cache)
}

// Invalidate forgets username's cached VFS.
func (r *Resolver) Invalidate(username string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, username)
}

// root returns the user's root mount. An account made since the server
// started (registration, an invitation) has none until this makes it, on
// the default volume, as Bootstrap does at startup.
func (r *Resolver) root(u *user.User) (*Mount, error) {
	m, err := r.mounts.GetUserRoot(u.ID)
	if !errors.Is(err, ErrNotFound) {
		return m, err
	}
	v, err := r.volumes.GetByName(volume.DefaultVolumeName)
	if err != nil {
		return nil, fmt.Errorf("root mount for %q: default volume: %w", u.Username, err)
	}
	return ensureRoot(r.mounts, v.ID, u)
}

// UserFilesDir looks up the user's root mount and resolves the concrete filesystem path.
func (r *Resolver) UserFilesDir(username string) (string, error) {
	u, err := r.users.GetByUsername(username)
	if err != nil {
		return "", fmt.Errorf("resolve user %q: %w", username, err)
	}

	m, err := r.root(u)
	if err != nil {
		return "", fmt.Errorf("resolve root mount for %q: %w", username, err)
	}

	v, err := r.volumes.GetByID(m.VolumeID)
	if err != nil {
		return "", fmt.Errorf("resolve volume %q: %w", m.VolumeID, err)
	}

	parsed, err := volume.ParseURI(v.URI)
	if err != nil {
		return "", fmt.Errorf("parse volume URI: %w", err)
	}

	return parsed.ResolvePath(m.VolumeSubpath)
}

// EnsureUserDirs creates the directory for the user's root mount.
func (r *Resolver) EnsureUserDirs(username string) error {
	u, err := r.users.GetByUsername(username)
	if err != nil {
		return fmt.Errorf("resolve user %q: %w", username, err)
	}

	m, err := r.root(u)
	if err != nil {
		return fmt.Errorf("resolve root mount for %q: %w", username, err)
	}

	v, err := r.volumes.GetByID(m.VolumeID)
	if err != nil {
		return fmt.Errorf("resolve volume %q: %w", m.VolumeID, err)
	}

	parsed, err := volume.ParseURI(v.URI)
	if err != nil {
		return fmt.Errorf("parse volume URI: %w", err)
	}

	return parsed.EnsureDir(m.VolumeSubpath)
}

// UserFS returns a mount-aware VFS for the given user, built from their
// mounts and kept until something changes or cacheTTL passes.
func (r *Resolver) UserFS(username string) (vfs.FS, error) {
	return r.userFS(username, 0)
}

// userFS is UserFS for a tree depth share mounts in. Only a whole tree is
// kept: one built deeper may lack share mounts maxShareDepth cut off.
func (r *Resolver) userFS(username string, depth int) (vfs.FS, error) {
	now := time.Now()
	r.mu.Lock()
	c, ok := r.cache[username]
	r.mu.Unlock()
	if ok && now.Before(c.expires) {
		return c.fs, nil
	}
	fs, err := r.buildUserFS(username, depth)
	if err != nil {
		return nil, err
	}
	if depth == 0 {
		r.mu.Lock()
		r.cache[username] = cachedFS{fs: fs, expires: now.Add(cacheTTL)}
		r.mu.Unlock()
	}
	return fs, nil
}

// buildUserFS loads the user's mounts and creates a backend for each.
func (r *Resolver) buildUserFS(username string, depth int) (*volume.MountFS, error) {
	u, err := r.users.GetByUsername(username)
	if err != nil {
		return nil, fmt.Errorf("resolve user %q: %w", username, err)
	}

	if _, err := r.root(u); err != nil {
		return nil, err
	}
	var entries []volume.MountEntry
	for m, err := range r.mounts.ListByUser(u.ID) {
		if err != nil {
			return nil, fmt.Errorf("list mounts for %q: %w", username, err)
		}

		if m.Kind == Share {
			if depth >= maxShareDepth {
				continue
			}
			e, ok, err := r.shareEntry(u, &m, depth)
			if err != nil {
				return nil, fmt.Errorf("mount %q: %w", m.UserPath, err)
			}
			if ok {
				entries = append(entries, e)
			}
			continue
		}

		v, err := r.volumes.GetByID(m.VolumeID)
		if err != nil {
			return nil, fmt.Errorf("resolve volume %q for mount %q: %w", m.VolumeID, m.UserPath, err)
		}

		readOnly := m.ReadOnly
		if m.UserPath != "/" {
			// The root mount is the one the server made for the user. Any
			// other reaches a volume only while the user may: a share
			// revoked, or narrowed to read-only, takes effect here.
			perms, err := r.mountPermissions(u, v, &m)
			if err != nil {
				return nil, fmt.Errorf("check access to volume %q for mount %q: %w", v.Name, m.UserPath, err)
			}
			if perms&plugin.PermRead == 0 {
				continue
			}
			readOnly = readOnly || perms&plugin.PermWrite == 0
		}

		backend, err := volume.NewBackend(v)
		if err != nil {
			return nil, fmt.Errorf("create backend for volume %q: %w", v.Name, err)
		}

		entries = append(entries, volume.MountEntry{
			UserPath:      m.UserPath,
			VolumeSubpath: m.VolumeSubpath,
			Backend:       backend,
			ReadOnly:      readOnly,
			VolumeID:      v.ID,
			ChargeTo:      u.Username,
		})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no mounts for user %q", username)
	}

	return volume.NewMountFS(entries), nil
}

// mountPermissions returns what a mount of v other than the root may do:
// an area, read and write while its storage volume is shared with the user;
// a mount of the volume itself, what they may do with all of it.
func (r *Resolver) mountPermissions(u *user.User, v *volume.Volume, m *Mount) (uint32, error) {
	if m.Kind == Area {
		ok, err := r.access.MayMakeAreas(u.ID, v)
		if err != nil || !ok {
			return 0, err
		}
		return plugin.PermRead | plugin.PermWrite, nil
	}
	return r.access.WholePermissions(u.ID, v)
}

// shareEntry resolves a share mount to where the shared folder is in its
// owner's tree, as the owner's shares of it allow u. It is not mounted
// (false) while they do not, or the folder or its owner is gone.
func (r *Resolver) shareEntry(u *user.User, m *Mount, depth int) (volume.MountEntry, bool, error) {
	owner, err := r.users.GetByID(m.ShareOwnerID)
	if errors.Is(err, user.ErrNotFound) {
		return volume.MountEntry{}, false, nil
	}
	if err != nil {
		return volume.MountEntry{}, false, fmt.Errorf("owner of shared folder: %w", err)
	}
	// Only the owner's shares count: the folder is found in their files.
	decision, err := r.access.Shares.CheckAccessGrantedBy(u.ID, SharedDirType, m.ShareResourceID,
		func(id string) (bool, error) { return id == owner.ID, nil })
	if err != nil {
		return volume.MountEntry{}, false, fmt.Errorf("check access to shared folder: %w", err)
	}
	perms := decision.EffectivePerms
	if perms&plugin.PermRead == 0 || r.DirPath == nil {
		return volume.MountEntry{}, false, nil
	}
	dir, err := r.DirPath(owner.Username, m.ShareResourceID)
	if err != nil {
		return volume.MountEntry{}, false, fmt.Errorf("find shared folder in %s's files: %w", owner.Username, err)
	}
	if dir == "" {
		return volume.MountEntry{}, false, nil
	}
	ownerFS, err := r.userFS(owner.Username, depth+1)
	if err != nil {
		return volume.MountEntry{}, false, fmt.Errorf("files of %s: %w", owner.Username, err)
	}
	mfs, ok := ownerFS.(*volume.MountFS)
	if !ok {
		return volume.MountEntry{}, false, fmt.Errorf("files of %s are a %T", owner.Username, ownerFS)
	}
	e, err := mfs.Entry(dir)
	if err != nil {
		return volume.MountEntry{}, false, nil // the folder is no longer in a mount of theirs
	}
	e.UserPath = m.UserPath
	e.ReadOnly = e.ReadOnly || perms&plugin.PermWrite == 0
	e.ShareOwner = owner.Username
	e.ShareOwnerPath = dir
	e.Reshare = perms&plugin.PermReshare != 0
	return e, true, nil
}
