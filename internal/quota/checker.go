package quota

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/user"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// UsageFunc returns the storage usage in bytes for a user.
type UsageFunc func(username string) (int64, error)

// RootVolumeFunc returns the root volume ID for a user, or empty string.
type RootVolumeFunc func(username string) (string, error)

// GroupIDsFunc returns the group IDs for a user.
type GroupIDsFunc func(userID string) ([]string, error)

// UserIDFunc looks up a user ID from a username.
type UserIDFunc func(username string) (string, error)

// Checker implements quota enforcement.
type Checker struct {
	Quotas         *Store
	Usage          UsageFunc
	RootVolume     RootVolumeFunc
	GroupIDs       GroupIDsFunc
	UserIDFromName UserIDFunc
}

// QuotaCheck checks if a user has enough quota for additionalBytes.
func (c *Checker) QuotaCheck(username string, additionalBytes int64) error {
	if c.Quotas == nil || c.RootVolume == nil {
		return nil
	}

	// A user with no root mount (or no account) has no volume to fill, so
	// there is nothing to enforce. Any other failure is reported: allowing
	// the write would let a database error lift the quota.
	volumeID, err := c.RootVolume(username)
	if errors.Is(err, mount.ErrNotFound) || errors.Is(err, user.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("quota check: root volume of %s: %w", username, err)
	}
	if volumeID == "" {
		return nil
	}

	userID, err := c.UserIDFromName(username)
	if errors.Is(err, user.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("quota check: user id of %s: %w", username, err)
	}

	var groupIDs []string
	if c.GroupIDs != nil {
		if groupIDs, err = c.GroupIDs(userID); err != nil {
			return fmt.Errorf("quota check: groups of %s: %w", username, err)
		}
	}

	quota, err := c.Quotas.EffectiveQuota(userID, volumeID, groupIDs)
	if err != nil {
		return fmt.Errorf("quota check: %w", err)
	}
	if quota < 0 {
		return nil // unlimited
	}

	var used int64
	if c.Usage != nil {
		used, err = c.Usage(username)
		if err != nil {
			return fmt.Errorf("quota check: %w", err)
		}
	}

	if used+additionalBytes > quota {
		return fmt.Errorf("%w: would use %s of %s limit",
			cogplugin.ErrQuotaExceeded, FormatBytes(used+additionalBytes), FormatBytes(quota))
	}
	return nil
}
