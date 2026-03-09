package mount

import (
	"errors"
	"fmt"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

// Bootstrap ensures a default volume exists and that every user has a root mount.
// It is idempotent and safe to call on every startup.
func Bootstrap(dataDir string, users *user.Store, volumes *volume.Store, mounts *Store) error {
	// Ensure default volume exists.
	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return fmt.Errorf("resolve data dir: %w", err)
	}

	defaultVol, err := volumes.GetByName("default")
	if errors.Is(err, volume.ErrNotFound) {
		uri := "file://" + absData
		defaultVol, err = volumes.Create("default", "Default local storage", uri, nil, "{}")
		if err != nil {
			return fmt.Errorf("create default volume: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("check default volume: %w", err)
	}

	// Ensure every user has a root mount.
	for u, err := range users.List() {
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		_, err = mounts.GetUserRoot(u.ID)
		if errors.Is(err, ErrNotFound) {
			subpath := u.Username + "/files"
			if _, err := mounts.Create(u.ID, "/", defaultVol.ID, subpath, false); err != nil {
				return fmt.Errorf("create root mount for %q: %w", u.Username, err)
			}
		} else if err != nil {
			return fmt.Errorf("check root mount for %q: %w", u.Username, err)
		}
	}

	return nil
}
