package mount

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"
)

// Resolver bridges the mount system to the plugin registry.
type Resolver struct {
	users   *user.Store
	mounts  *Store
	volumes *volume.Store
	access  *volume.Access
}

// NewResolver creates a new mount resolver. Access decides what a mount of a
// volume other than the user's root may do: a mount outlives the share that
// allowed it, so it is checked each time it is used.
func NewResolver(users *user.Store, mounts *Store, volumes *volume.Store, access *volume.Access) *Resolver {
	return &Resolver{users: users, mounts: mounts, volumes: volumes, access: access}
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

// UserFS builds a mount-aware VFS for the given user, loading all mounts
// and creating backends for each.
func (r *Resolver) UserFS(username string) (vfs.FS, error) {
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

		v, err := r.volumes.GetByID(m.VolumeID)
		if err != nil {
			return nil, fmt.Errorf("resolve volume %q for mount %q: %w", m.VolumeID, m.UserPath, err)
		}

		readOnly := m.ReadOnly
		if m.UserPath != "/" {
			// The root mount is the one the server made for the user. Any
			// other reaches a volume only while the user may: a share
			// revoked, or narrowed to read-only, takes effect here.
			perms, err := r.access.Permissions(u.ID, v.ID)
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
		})
	}

	if len(entries) == 0 {
		return nil, fmt.Errorf("no mounts for user %q", username)
	}

	return volume.NewMountFS(entries), nil
}
