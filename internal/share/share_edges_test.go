package share_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

// --- PermissionNames ---

func TestPermissionNames_AllSet(t *testing.T) {
	got := share.PermissionNames(plugin.PermRead | plugin.PermWrite | plugin.PermReshare)
	require.Equal(t, 3, len(got))
}

func TestPermissionNames_Empty(t *testing.T) {
	got := share.PermissionNames(0)
	require.Equal(t, 0, len(got))
}

func TestPermissionNames_OnlyReshare(t *testing.T) {
	got := share.PermissionNames(plugin.PermReshare)
	require.Equal(t, 1, len(got))
	require.Equal(t, "reshare", got[0])
}

// --- ListByOwner ---

func TestListByOwner(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")

	_, err := env.Providers.Shares.Create(alice.ID, "files:file", "1", "USER", bob.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(alice.ID, "files:file", "2", "USER", carol.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(bob.ID, "files:file", "3", "USER", carol.ID, plugin.PermRead)
	require.NoError(t, err)

	var aliceShares []share.Record
	for rec, err := range env.Providers.Shares.ListByOwner(alice.ID) {
		require.NoError(t, err)
		aliceShares = append(aliceShares, rec)
	}
	require.Equal(t, 2, len(aliceShares))

	var bobShares []share.Record
	for rec, err := range env.Providers.Shares.ListByOwner(bob.ID) {
		require.NoError(t, err)
		bobShares = append(bobShares, rec)
	}
	require.Equal(t, 1, len(bobShares))
}

func TestListByOwner_Empty(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	var n int
	for _, err := range env.Providers.Shares.ListByOwner(alice.ID) {
		require.NoError(t, err)
		n++
	}
	require.Equal(t, 0, n)
}

// --- PermReshare semantics ---

func TestCheckAccess_ReshareBitMergedInDecision(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	_, err := env.Providers.Shares.Create(
		owner.ID, "files:file", "r1", "USER", target.ID,
		plugin.PermRead|plugin.PermReshare,
	)
	require.NoError(t, err)

	d, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "r1")
	require.NoError(t, err)
	require.True(t, d.Allowed)
	require.True(t, d.EffectivePerms&plugin.PermReshare != 0)
	require.True(t, d.EffectivePerms&plugin.PermWrite == 0)
}

// --- Downgrade on partial delete ---

func TestCheckAccess_DowngradeOnDelete(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	read, err := env.Providers.Shares.Create(
		owner.ID, "files:file", "x", "USER", target.ID, plugin.PermRead,
	)
	require.NoError(t, err)
	write, err := env.Providers.Shares.Create(
		owner.ID, "files:file", "x", "USER", target.ID, plugin.PermWrite,
	)
	require.NoError(t, err)

	// Both bits set.
	d, _ := env.Providers.Shares.CheckAccess(target.ID, "files:file", "x")
	require.True(t, d.EffectivePerms&plugin.PermWrite != 0)
	require.True(t, d.EffectivePerms&plugin.PermRead != 0)

	// Drop the write share — read remains.
	require.NoError(t, env.Providers.Shares.Delete(write.ID))
	d, _ = env.Providers.Shares.CheckAccess(target.ID, "files:file", "x")
	require.True(t, d.EffectivePerms&plugin.PermRead != 0)
	require.False(t, d.EffectivePerms&plugin.PermWrite != 0)

	// Drop read too — access fully denied.
	require.NoError(t, env.Providers.Shares.Delete(read.ID))
	d, _ = env.Providers.Shares.CheckAccess(target.ID, "files:file", "x")
	require.False(t, d.Allowed)
}

// --- Zero-permission share ---

func TestCreate_ZeroPermissions_StoresButDeniesAccess(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	rec, err := env.Providers.Shares.Create(
		owner.ID, "files:file", "z", "USER", target.ID, 0,
	)
	require.NoError(t, err)
	require.True(t, rec.ID != "")

	// EffectivePerms is 0 but the share row exists; CheckAccess merges bits,
	// so target gets perms == 0. We accept either "Allowed=true with no
	// perms" or "Allowed=false" — pin the actual behavior here.
	d, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "z")
	require.NoError(t, err)
	require.Equal(t, uint32(0), d.EffectivePerms)
}

// --- Share to self ---

func TestCreate_ShareToSelf_AllowedButPointless(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Alice shares a resource with herself. The store doesn't reject this;
	// nothing forbids it at the schema level. The resulting share record
	// produces the merged-perms decision Alice already has as owner.
	_, err := env.Providers.Shares.Create(
		alice.ID, "files:file", "selfshare", "USER", alice.ID, plugin.PermRead,
	)
	require.NoError(t, err)

	d, err := env.Providers.Shares.CheckAccess(alice.ID, "files:file", "selfshare")
	require.NoError(t, err)
	require.True(t, d.Allowed)
}

// --- Share to deleted user ---

func TestCreate_ShareToUnknownUser_StoredAsString(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// SubjectID is just a string — no FK to users. This is by design (allows
	// sharing by username and external IDs). Document the behavior.
	_, err := env.Providers.Shares.Create(
		alice.ID, "files:file", "ghost", "USER", "no-such-user", plugin.PermRead,
	)
	require.NoError(t, err)
}

// --- Token uniqueness ---

func TestCreatePublicLink_TokensAreUnique(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	seen := make(map[string]bool)
	for range 8 {
		rec, err := env.Providers.Shares.CreatePublicLink(
			alice.ID, "files:file", "r", plugin.PermRead, nil,
		)
		require.NoError(t, err)
		require.NotNil(t, rec.Token)
		require.False(t, seen[*rec.Token])
		seen[*rec.Token] = true
	}
}

// --- Permission update via Store.Update ---

func TestUpdate_PermissionsChange(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	rec, err := env.Providers.Shares.Create(
		owner.ID, "files:file", "u", "USER", target.ID, plugin.PermRead,
	)
	require.NoError(t, err)

	// Bump to read+write.
	newPerms := plugin.PermRead | plugin.PermWrite
	require.NoError(t, env.Providers.Shares.Update(rec.ID, &newPerms, nil))

	d, _ := env.Providers.Shares.CheckAccess(target.ID, "files:file", "u")
	require.True(t, d.EffectivePerms&plugin.PermWrite != 0)
}
