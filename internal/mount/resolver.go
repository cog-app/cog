package mount

import (
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

// Resolver bridges the mount system to the plugin registry.
type Resolver struct {
	users   *user.Store
	mounts  *Store
	volumes *volume.Store
}

// NewResolver creates a new mount resolver.
func NewResolver(users *user.Store, mounts *Store, volumes *volume.Store) *Resolver {
	return &Resolver{users: users, mounts: mounts, volumes: volumes}
}

// UserFilesDir looks up the user's root mount and resolves the concrete filesystem path.
func (r *Resolver) UserFilesDir(username string) (string, error) {
	u, err := r.users.GetByUsername(username)
	if err != nil {
		return "", fmt.Errorf("resolve user %q: %w", username, err)
	}

	m, err := r.mounts.GetUserRoot(u.ID)
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

	m, err := r.mounts.GetUserRoot(u.ID)
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
