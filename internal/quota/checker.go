package quota

import (
	"errors"
	"fmt"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/quota/budget"
	"git.duckfam.us/jj-software/cog/internal/user"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// Account is what a quota holds: a user's bytes on a volume. A zero
// VolumeID is their root volume, where their files and everything plugins
// keep for them are.
type Account struct {
	Username string
	VolumeID string
}

// Root returns username's account on their root volume.
func Root(username string) Account { return Account{Username: username} }

// key names the account in the budgets: the username alone for the root
// volume, so that every writer of it shares one budget.
func (a Account) key() string {
	if a.VolumeID == "" {
		return a.Username
	}
	return a.Username + "\x00" + a.VolumeID
}

func accountOf(key string) Account {
	name, vol, _ := strings.Cut(key, "\x00")
	return Account{Username: name, VolumeID: vol}
}

// UsageFunc returns the bytes an account holds.
type UsageFunc func(a Account) (int64, error)

// AccountAtFunc returns the account a write to path in username's files is
// charged to: theirs on the volume it lands on, or, in a folder shared with
// them, its owner's.
type AccountAtFunc func(username, path string) (Account, error)

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
	// AccountAt says whose quota a write to a path counts against; without
	// it every write is the user's own, on their root volume.
	AccountAt AccountAtFunc
	// Writes is the budget writes in progress share; what they have
	// written counts against the quota before it is recorded in usage.
	Writes *budget.Budgets
}

// NewWrites gives c the budget writes in progress share, started from what
// recorded usage leaves.
func (c *Checker) NewWrites() {
	c.Writes = budget.New(func(key string) (int64, error) {
		quota, used, limited, err := c.limit(accountOf(key))
		if err != nil || !limited {
			return -1, err
		}
		return max(quota-used, 0), nil
	})
}

// BeginWrite starts a write for username, drawing on their budget.
func (c *Checker) BeginWrite(username string) (*budget.Write, error) {
	return c.BeginWriteFor(Root(username))
}

// BeginWriteFor starts a write charged to a, drawing on its budget.
func (c *Checker) BeginWriteFor(a Account) (*budget.Write, error) {
	return c.Writes.Begin(a.key())
}

// Account returns the account a write to path in username's files is
// charged to.
func (c *Checker) Account(username, path string) (Account, error) {
	if c.AccountAt == nil {
		return Root(username), nil
	}
	return c.AccountAt(username, path)
}

// BeginWriteAt starts a write to path in username's files, charged to
// whoever that path charges.
func (c *Checker) BeginWriteAt(username, path string) (*budget.Write, error) {
	a, err := c.Account(username, path)
	if err != nil {
		return nil, fmt.Errorf("quota account of %s: %w", path, err)
	}
	return c.BeginWriteFor(a)
}

// QuotaCheckAt checks there is room for additionalBytes more at path in
// username's files.
func (c *Checker) QuotaCheckAt(username, path string, additionalBytes int64) error {
	a, err := c.Account(username, path)
	if err != nil {
		return fmt.Errorf("quota account of %s: %w", path, err)
	}
	return c.QuotaCheckFor(a, additionalBytes)
}

// QuotaCheck checks if a user has enough quota for additionalBytes.
func (c *Checker) QuotaCheck(username string, additionalBytes int64) error {
	return c.QuotaCheckFor(Root(username), additionalBytes)
}

// QuotaCheckFor checks if the account has room for additionalBytes more.
func (c *Checker) QuotaCheckFor(a Account, additionalBytes int64) error {
	quota, used, limited, err := c.limit(a)
	if err != nil || !limited {
		return err
	}
	used += c.Writes.Pending(a.key())
	if used+additionalBytes > quota {
		return fmt.Errorf("%w: would use %s of %s limit",
			cogplugin.ErrQuotaExceeded, FormatBytes(used+additionalBytes), FormatBytes(quota))
	}
	return nil
}

// Remaining returns how many more bytes username may store, or -1 when
// nothing limits them; zero when they are at or over quota.
func (c *Checker) Remaining(username string) (int64, error) {
	return c.RemainingFor(Root(username))
}

// RemainingFor is Remaining for an account.
func (c *Checker) RemainingFor(a Account) (int64, error) {
	quota, used, limited, err := c.limit(a)
	if err != nil || !limited {
		return -1, err
	}
	return max(quota-used-c.Writes.Pending(a.key()), 0), nil
}

// limit returns the account's quota and what it holds, and whether a quota
// applies at all.
func (c *Checker) limit(a Account) (quota, used int64, limited bool, err error) {
	if c.Quotas == nil || c.RootVolume == nil {
		return 0, 0, false, nil
	}
	username := a.Username

	// A user with no root mount (or no account) has no volume to fill, so
	// there is nothing to enforce. Any other failure is reported: allowing
	// the write would let a database error lift the quota.
	volumeID := a.VolumeID
	if volumeID == "" {
		volumeID, err = c.RootVolume(username)
		if errors.Is(err, mount.ErrNotFound) || errors.Is(err, user.ErrNotFound) {
			return 0, 0, false, nil
		}
		if err != nil {
			return 0, 0, false, fmt.Errorf("quota check: root volume of %s: %w", username, err)
		}
		if volumeID == "" {
			return 0, 0, false, nil
		}
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
		used, err = c.Usage(a)
		if err != nil {
			return 0, 0, false, fmt.Errorf("quota check: %w", err)
		}
	}
	return quota, used, true, nil
}
