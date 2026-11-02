package share_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
)

func TestCreateAndGet(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", "target1", plugin.PermRead)
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, "USER", rec.SubjectType)
	require.Equal(t, plugin.PermRead, rec.Permissions)

	got, err := env.Providers.Shares.Get(rec.ID)
	require.NoError(t, err)
	require.Equal(t, rec.ID, got.ID)
	require.Equal(t, owner.ID, got.OwnerUserID)
	require.Equal(t, "files:file", got.ResourceType)
}

func TestCreatePublicLink(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.CreatePublicLink(owner.ID, "files:file", "res1", plugin.PermRead, nil)
	require.NoError(t, err)
	require.Equal(t, "PUBLIC", rec.SubjectType)
	require.NotNil(t, rec.Token)
	require.True(t, len(*rec.Token) > 0)
}

func TestPublicLinkPassword(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.CreatePublicLinkWithPassword(owner.ID, "files:file", "res1", plugin.PermRead, nil, "secret123")
	require.NoError(t, err)

	// Re-fetch to get password hash.
	got, err := env.Providers.Shares.Get(rec.ID)
	require.NoError(t, err)

	// Correct password.
	err = share.CheckPassword(got, "secret123")
	require.NoError(t, err)

	// Incorrect password.
	err = share.CheckPassword(got, "wrong")
	require.Error(t, err)
	require.Equal(t, share.ErrPasswordIncorrect, err)

	// No password provided.
	err = share.CheckPassword(got, "")
	require.Error(t, err)
	require.Equal(t, share.ErrPasswordRequired, err)
}

func TestGetByToken(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.CreatePublicLink(owner.ID, "files:file", "res1", plugin.PermRead, nil)
	require.NoError(t, err)

	got, err := env.Providers.Shares.GetByToken(*rec.Token)
	require.NoError(t, err)
	require.Equal(t, rec.ID, got.ID)

	// Not found.
	_, err = env.Providers.Shares.GetByToken("nonexistent")
	require.Error(t, err)
	require.Equal(t, share.ErrNotFound, err)
}

func TestGetByTokenExpired(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	past := env.Clock.Now().Add(-1 * time.Hour).Unix()
	rec, err := env.Providers.Shares.CreatePublicLink(owner.ID, "files:file", "res1", plugin.PermRead, &past)
	require.NoError(t, err)

	_, err = env.Providers.Shares.GetByToken(*rec.Token)
	require.Error(t, err)
	require.Equal(t, share.ErrNotFound, err)
}

func TestCheckAccess(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermRead|plugin.PermWrite)
	require.NoError(t, err)

	decision, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "res1")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, plugin.PermRead|plugin.PermWrite, decision.EffectivePerms)

	// Non-shared user has no access.
	other := env.CreateUser(t, "other", "pass")
	decision, err = env.Providers.Shares.CheckAccess(other.ID, "files:file", "res1")
	require.NoError(t, err)
	require.False(t, decision.Allowed)
}

func TestCheckAccessGroup(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	member := env.CreateUser(t, "member", "pass")

	// Create group and add member.
	grp, err := env.Providers.Groups.Create("testgroup", nil, &owner.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, member.ID, "USER"))

	// Share to group.
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "res1", "GROUP", grp.ID, plugin.PermRead)
	require.NoError(t, err)

	// Group member has access via closure table.
	decision, err := env.Providers.Shares.CheckAccess(member.ID, "files:file", "res1")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestCheckAccessExpired(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	past := env.Clock.Now().Add(-1 * time.Hour).Unix()
	rec, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermRead)
	require.NoError(t, err)

	// Set expiration in the past.
	pastPtr := &past
	require.NoError(t, env.Providers.Shares.Update(rec.ID, nil, &pastPtr))

	decision, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "res1")
	require.NoError(t, err)
	require.False(t, decision.Allowed)
}

func TestCheckAccessPermissionMerge(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	// Two shares with different permissions.
	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermWrite)
	require.NoError(t, err)

	decision, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "res1")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
	require.Equal(t, plugin.PermRead|plugin.PermWrite, decision.EffectivePerms)
	require.Equal(t, 2, len(decision.MatchedShareIDs))
}

func TestListBySubject(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "res2", "USER", target.ID, plugin.PermWrite)
	require.NoError(t, err)

	var records []share.Record
	for rec, err := range env.Providers.Shares.ListBySubject(target.ID) {
		require.NoError(t, err)
		records = append(records, rec)
	}
	require.Equal(t, 2, len(records))
}

func TestListByResource(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u1 := env.CreateUser(t, "user1", "pass")
	u2 := env.CreateUser(t, "user2", "pass")

	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", u1.ID, plugin.PermRead)
	require.NoError(t, err)
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", u2.ID, plugin.PermRead)
	require.NoError(t, err)

	var records []share.Record
	for rec, err := range env.Providers.Shares.ListByResource("files:file", "res1") {
		require.NoError(t, err)
		records = append(records, rec)
	}
	require.Equal(t, 2, len(records))
}

func TestInstanceShareAccess(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	user1 := env.CreateUser(t, "user1", "pass")
	user2 := env.CreateUser(t, "user2", "pass")

	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res-inst", "INSTANCE", "", plugin.PermRead)
	require.NoError(t, err)

	// Any authenticated user should have access.
	decision, err := env.Providers.Shares.CheckAccess(user1.ID, "files:file", "res-inst")
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	decision, err = env.Providers.Shares.CheckAccess(user2.ID, "files:file", "res-inst")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestInstanceShareListBySubject(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	user1 := env.CreateUser(t, "user1", "pass")

	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "res-inst2", "INSTANCE", "", plugin.PermRead)
	require.NoError(t, err)

	// INSTANCE shares should appear in any user's ListBySubject.
	var found bool
	for rec, err := range env.Providers.Shares.ListBySubject(user1.ID) {
		require.NoError(t, err)
		if rec.SubjectType == "INSTANCE" {
			found = true
		}
	}
	require.True(t, found)
}

func TestCreateInstanceShare(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.Create(owner.ID, "files:file", "res-inst3", "INSTANCE", "", plugin.PermRead)
	require.NoError(t, err)
	require.Equal(t, "INSTANCE", rec.SubjectType)
	require.Equal(t, "", rec.SubjectID)
}

func TestDelete(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	rec, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", target.ID, plugin.PermRead)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Shares.Delete(rec.ID))

	decision, err := env.Providers.Shares.CheckAccess(target.ID, "files:file", "res1")
	require.NoError(t, err)
	require.False(t, decision.Allowed)
}

func TestCheckAccessWithHierarchy(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	target := env.CreateUser(t, "target", "pass")

	// Share parent resource.
	_, err := env.Providers.Shares.Create(owner.ID, "files:file", "parent-dir", "USER", target.ID, plugin.PermRead)
	require.NoError(t, err)

	// parentLookup: child -> parent-dir, parent-dir -> ""
	parentLookup := func(resourceID string) (string, error) {
		if resourceID == "child-file" {
			return "parent-dir", nil
		}
		return "", nil
	}

	// Child inherits access from parent.
	decision, err := env.Providers.Shares.CheckAccessWithHierarchy(target.ID, "files:file", "child-file", parentLookup)
	require.NoError(t, err)
	require.True(t, decision.Allowed)

	// Unshared user gets denied.
	other := env.CreateUser(t, "other", "pass")
	decision, err = env.Providers.Shares.CheckAccessWithHierarchy(other.ID, "files:file", "child-file", parentLookup)
	require.NoError(t, err)
	require.False(t, decision.Allowed)
}

func TestNCIDRoundtrip(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	rec, err := env.Providers.Shares.Create(owner.ID, "files:file", "res1", "USER", "target1", plugin.PermRead)
	require.NoError(t, err)

	// First call creates the NC ID.
	ncID, err := env.Providers.Shares.NCIDFor(rec.ID)
	require.NoError(t, err)
	require.True(t, ncID > 0)

	// Second call returns the same ID.
	ncID2, err := env.Providers.Shares.NCIDFor(rec.ID)
	require.NoError(t, err)
	require.Equal(t, ncID, ncID2)

	// Reverse lookup.
	shareID, err := env.Providers.Shares.ShareIDFromNC(ncID)
	require.NoError(t, err)
	require.Equal(t, rec.ID, shareID)

	// Not found.
	_, err = env.Providers.Shares.ShareIDFromNC(999999)
	require.Error(t, err)
	require.Equal(t, share.ErrNotFound, err)
}

// NCIDsFor keeps ids NCIDFor already handed out and creates the rest.
func TestNCIDsForBatch(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	shares := env.Providers.Shares

	a, err := shares.Create(owner.ID, "files:file", "a", "USER", "t", plugin.PermRead)
	require.NoError(t, err)
	b, err := shares.Create(owner.ID, "files:file", "b", "USER", "t", plugin.PermRead)
	require.NoError(t, err)

	existing, err := shares.NCIDFor(a.ID)
	require.NoError(t, err)

	got, err := shares.NCIDsFor([]string{a.ID, b.ID})
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
	require.Equal(t, existing, got[a.ID])

	again, err := shares.NCIDFor(b.ID)
	require.NoError(t, err)
	require.Equal(t, got[b.ID], again)

	empty, err := shares.NCIDsFor(nil)
	require.NoError(t, err)
	require.Equal(t, 0, len(empty))
}
