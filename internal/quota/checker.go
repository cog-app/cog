package quota

import (
	"fmt"

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

	volumeID, err := c.RootVolume(username)
	if err != nil || volumeID == "" {
		return nil // can't resolve, allow
	}

	userID, err := c.UserIDFromName(username)
	if err != nil {
		return nil // can't resolve, allow
	}

	var groupIDs []string
	if c.GroupIDs != nil {
		groupIDs, _ = c.GroupIDs(userID)
	}

	quota := c.Quotas.EffectiveQuota(userID, volumeID, groupIDs)
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
