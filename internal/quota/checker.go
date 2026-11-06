package quota

import (
	"errors"
	"fmt"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/quota/budget"
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
	// Writes is the budget writes in progress share; what they have
	// written counts against the quota before it is recorded in usage.
	Writes *budget.Budgets
}

// NewWrites gives c the budget writes in progress share, started from what
// recorded usage leaves.
func (c *Checker) NewWrites() {
	c.Writes = budget.New(func(username string) (int64, error) {
		quota, used, limited, err := c.limit(username)
		if err != nil || !limited {
			return -1, err
		}
		return max(quota-used, 0), nil
	})
}

// BeginWrite starts a write for username, drawing on their budget.
func (c *Checker) BeginWrite(username string) (*budget.Write, error) {
	return c.Writes.Begin(username)
}

// QuotaCheck checks if a user has enough quota for additionalBytes.
func (c *Checker) QuotaCheck(username string, additionalBytes int64) error {
	quota, used, limited, err := c.limit(username)
	if err != nil || !limited {
		return err
	}
	used += c.Writes.Pending(username)
	if used+additionalBytes > quota {
		return fmt.Errorf("%w: would use %s of %s limit",
			cogplugin.ErrQuotaExceeded, FormatBytes(used+additionalBytes), FormatBytes(quota))
	}
	return nil
}

// Remaining returns how many more bytes username may store, or -1 when
// nothing limits them; zero when they are at or over quota.
func (c *Checker) Remaining(username string) (int64, error) {
	quota, used, limited, err := c.limit(username)
	if err != nil || !limited {
		return -1, err
	}
	return max(quota-used-c.Writes.Pending(username), 0), nil
}

// limit returns username's quota and what they use, and whether a quota
// applies at all.
func (c *Checker) limit(username string) (quota, used int64, limited bool, err error) {
	if c.Quotas == nil || c.RootVolume == nil {
		return 0, 0, false, nil
	}

	// A user with no root mount (or no account) has no volume to fill, so
	// there is nothing to enforce. Any other failure is reported: allowing
	// the write would let a database error lift the quota.
	volumeID, err := c.RootVolume(username)
	if errors.Is(err, mount.ErrNotFound) || errors.Is(err, user.ErrNotFound) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("quota check: root volume of %s: %w", username, err)
	}
	if volumeID == "" {
		return 0, 0, false, nil
	}

	userID, err := c.UserIDFromName(username)
	if errors.Is(err, user.ErrNotFound) {
		return 0, 0, false, nil
	}
	if err != nil {
		return 0, 0, false, fmt.Errorf("quota check: user id of %s: %w", username, err)
	}

	var groupIDs []string
	if c.GroupIDs != nil {
		if groupIDs, err = c.GroupIDs(userID); err != nil {
			return 0, 0, false, fmt.Errorf("quota check: groups of %s: %w", username, err)
		}
	}

	quota, err = c.Quotas.EffectiveQuota(userID, volumeID, groupIDs)
	if err != nil {
		return 0, 0, false, fmt.Errorf("quota check: %w", err)
	}
	if quota < 0 {
		return 0, 0, false, nil // unlimited
	}

	if c.Usage != nil {
		used, err = c.Usage(username)
		if err != nil {
			return 0, 0, false, fmt.Errorf("quota check: %w", err)
		}
	}
	return quota, used, true, nil
}
