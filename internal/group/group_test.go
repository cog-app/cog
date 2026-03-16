package group_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCreateAndGet(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)
	require.Equal(t, "devs", grp.Name)

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, grp.ID, got.ID)
	require.Equal(t, "devs", got.Name)
	require.Equal(t, 0, got.MemberCount)
}

func TestAddMember(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.MemberCount)

	var members []group.Member
	for m, err := range env.Providers.Groups.ListMembers(grp.ID) {
		require.NoError(t, err)
		members = append(members, m)
	}
	require.Equal(t, 1, len(members))
	require.Equal(t, u.ID, members[0].MemberID)
	require.Equal(t, group.User, members[0].MemberType)
}

func TestAddMemberIdempotent(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 1, got.MemberCount)
}

func TestRemoveMember(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))
	require.NoError(t, env.Providers.Groups.RemoveMember(grp.ID, u.ID))

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, 0, got.MemberCount)
}

func TestListGroupsForUser(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp1, err := env.Providers.Groups.Create("alpha", nil, &owner.ID)
	require.NoError(t, err)
	grp2, err := env.Providers.Groups.Create("beta", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp1.ID, u.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(grp2.ID, u.ID, group.User))

	var groups []group.Group
	for g, err := range env.Providers.Groups.ListGroupsForUser(u.ID) {
		require.NoError(t, err)
		groups = append(groups, g)
	}
	require.Equal(t, 2, len(groups))
}

func TestIsOwnerMember(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, owner.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))

	isOwner, err := env.Providers.Groups.IsOwnerMember(grp.ID, owner.ID)
	require.NoError(t, err)
	require.True(t, isOwner)

	isOwner, err = env.Providers.Groups.IsOwnerMember(grp.ID, u.ID)
	require.NoError(t, err)
	require.False(t, isOwner)
}

func TestUpdateMemberType(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))

	isOwner, err := env.Providers.Groups.IsOwnerMember(grp.ID, u.ID)
	require.NoError(t, err)
	require.False(t, isOwner)

	require.NoError(t, env.Providers.Groups.UpdateMemberType(grp.ID, u.ID, group.Owner))

	isOwner, err = env.Providers.Groups.IsOwnerMember(grp.ID, u.ID)
	require.NoError(t, err)
	require.True(t, isOwner)
}

func TestDeleteGroup(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))
	require.NoError(t, env.Providers.Groups.Delete(grp.ID))

	_, err = env.Providers.Groups.Get(grp.ID)
	require.Error(t, err)
	require.Equal(t, group.ErrNotFound, err)
}

func TestNestedGroupClosure(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	user1 := env.CreateUser(t, "user1", "pass")

	// A contains B (GROUP), B contains user1.
	grpA, err := env.Providers.Groups.Create("A", nil, &owner.ID)
	require.NoError(t, err)
	grpB, err := env.Providers.Groups.Create("B", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grpB.ID, user1.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(grpA.ID, grpB.ID, group.GroupMember))

	// user1 should appear in group A's closure.
	ids, err := env.Providers.Groups.ListClosureUserIDs(grpA.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(ids))
	require.Equal(t, user1.ID, ids[0])

	// Share with group A should be accessible to user1.
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "nested-res", "GROUP", grpA.ID, 1)
	require.NoError(t, err)

	decision, err := env.Providers.Shares.CheckAccess(user1.ID, "files:file", "nested-res")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestCycleDetection(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	grpA, err := env.Providers.Groups.Create("A", nil, &owner.ID)
	require.NoError(t, err)
	grpB, err := env.Providers.Groups.Create("B", nil, &owner.ID)
	require.NoError(t, err)

	// A contains B.
	require.NoError(t, env.Providers.Groups.AddMember(grpA.ID, grpB.ID, group.GroupMember))

	// Adding A to B should fail (cycle).
	err = env.Providers.Groups.AddMember(grpB.ID, grpA.ID, group.GroupMember)
	require.Error(t, err)
	require.Equal(t, group.ErrCyclicMembership, err)
}

func TestDeepNesting(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	user1 := env.CreateUser(t, "user1", "pass")

	grpA, err := env.Providers.Groups.Create("A", nil, &owner.ID)
	require.NoError(t, err)
	grpB, err := env.Providers.Groups.Create("B", nil, &owner.ID)
	require.NoError(t, err)
	grpC, err := env.Providers.Groups.Create("C", nil, &owner.ID)
	require.NoError(t, err)

	// A → B → C → user1
	require.NoError(t, env.Providers.Groups.AddMember(grpC.ID, user1.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(grpB.ID, grpC.ID, group.GroupMember))
	require.NoError(t, env.Providers.Groups.AddMember(grpA.ID, grpB.ID, group.GroupMember))

	// user1 should be in A's closure.
	ids, err := env.Providers.Groups.ListClosureUserIDs(grpA.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(ids))
	require.Equal(t, user1.ID, ids[0])
}

func TestNestedGroupShareAccess(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	user1 := env.CreateUser(t, "user1", "pass")

	grpA, err := env.Providers.Groups.Create("A", nil, &owner.ID)
	require.NoError(t, err)
	grpB, err := env.Providers.Groups.Create("B", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.AddMember(grpB.ID, user1.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(grpA.ID, grpB.ID, group.GroupMember))

	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "shared-nested", "GROUP", grpA.ID, 1)
	require.NoError(t, err)

	decision, err := env.Providers.Shares.CheckAccess(user1.ID, "files:file", "shared-nested")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}

func TestMemberTypeIsValid(t *testing.T) {
	if !group.Owner.IsValid() {
		t.Fatal("OWNER should be valid")
	}
	if !group.User.IsValid() {
		t.Fatal("USER should be valid")
	}
	if !group.GroupMember.IsValid() {
		t.Fatal("GROUP should be valid")
	}
	if group.MemberType("INVALID").IsValid() {
		t.Fatal("INVALID should not be valid")
	}
}

func TestUpdateGroup(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	grp, err := env.Providers.Groups.Create("old-name", nil, &owner.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.Update(grp.ID, "new-name"))

	got, err := env.Providers.Groups.Get(grp.ID)
	require.NoError(t, err)
	require.Equal(t, "new-name", got.Name)
}

func TestUpdateGroupNotFound(t *testing.T) {
	env := cogtest.New(t)

	err := env.Providers.Groups.Update("nonexistent", "whatever")
	require.Error(t, err)
	require.Equal(t, group.ErrNotFound, err)
}

func TestListOwnedByUser(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")

	grpA, err := env.Providers.Groups.Create("alpha", nil, &owner.ID)
	require.NoError(t, err)
	grpB, err := env.Providers.Groups.Create("beta", nil, &owner.ID)
	require.NoError(t, err)

	// owner is OWNER of A, regular USER of B.
	require.NoError(t, env.Providers.Groups.AddMember(grpA.ID, owner.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(grpB.ID, owner.ID, group.User))

	var owned []group.Group
	for g, err := range env.Providers.Groups.ListOwnedByUser(owner.ID) {
		require.NoError(t, err)
		owned = append(owned, g)
	}
	require.Equal(t, 1, len(owned))
	require.Equal(t, grpA.ID, owned[0].ID)
}

func TestRebuildClosure(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	u := env.CreateUser(t, "user1", "pass")

	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.User))

	// RebuildClosure should succeed and maintain correct data.
	require.NoError(t, env.Providers.Groups.RebuildClosure())

	// Verify closure still works — share to group, check access.
	_, err = env.Providers.Shares.Create(owner.ID, "files:file", "res1", "GROUP", grp.ID, 1)
	require.NoError(t, err)

	decision, err := env.Providers.Shares.CheckAccess(u.ID, "files:file", "res1")
	require.NoError(t, err)
	require.True(t, decision.Allowed)
}
