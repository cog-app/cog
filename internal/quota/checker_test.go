package quota_test

import (
	"errors"
	"testing"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"

	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCheckerWithinQuota(t *testing.T) {
	env := cogtest.New(t)
	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)
	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "USER", "uid1", 10000))

	c := &quota.Checker{
		Quotas:         env.Providers.Quotas,
		Usage:          func(quota.Account) (int64, error) { return 5000, nil },
		RootVolume:     func(string) (string, error) { return vol.ID, nil },
		GroupIDs:       func(string) ([]string, error) { return nil, nil },
		UserIDFromName: func(string) (string, error) { return "uid1", nil },
	}
	err = c.QuotaCheck("alice", 4000)
	require.NoError(t, err)
}

func TestCheckerExceedsQuota(t *testing.T) {
	env := cogtest.New(t)
	vol, err := env.Providers.Volumes.Create("testvol", "", "file:///tmp/test", nil, "{}")
	require.NoError(t, err)
	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "USER", "uid1", 10000))

	c := &quota.Checker{
		Quotas:         env.Providers.Quotas,
		Usage:          func(quota.Account) (int64, error) { return 5000, nil },
		RootVolume:     func(string) (string, error) { return vol.ID, nil },
		GroupIDs:       func(string) ([]string, error) { return nil, nil },
		UserIDFromName: func(string) (string, error) { return "uid1", nil },
	}
	err = c.QuotaCheck("alice", 6000)
	require.Error(t, err)
	require.True(t, errors.Is(err, cogplugin.ErrQuotaExceeded))
}

func TestCheckerUnlimited(t *testing.T) {
	env := cogtest.New(t)
	// No quotas set → unlimited.
	c := &quota.Checker{
		Quotas:         env.Providers.Quotas,
		Usage:          func(quota.Account) (int64, error) { return 999999, nil },
		RootVolume:     func(string) (string, error) { return "somevol", nil },
		GroupIDs:       func(string) ([]string, error) { return nil, nil },
		UserIDFromName: func(string) (string, error) { return "uid1", nil },
	}
	err := c.QuotaCheck("alice", 999999)
	require.NoError(t, err)
}

func TestCheckerNilDeps(t *testing.T) {
	// Nil Quotas → nil (graceful degradation).
	c := &quota.Checker{}
	err := c.QuotaCheck("alice", 1000)
	require.NoError(t, err)

	// Nil RootVolume → nil.
	c = &quota.Checker{Quotas: &quota.Store{}}
	err = c.QuotaCheck("alice", 1000)
	require.NoError(t, err)
}
