package group_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- List ---

func TestList_ReturnsAllGroups(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	owner := alice.ID

	_, err := env.Providers.Groups.Create("alpha", nil, &owner)
	require.NoError(t, err)
	_, err = env.Providers.Groups.Create("beta", nil, &owner)
	require.NoError(t, err)

	var got []group.Group
	for g, err := range env.Providers.Groups.List() {
		require.NoError(t, err)
		got = append(got, g)
	}
	require.True(t, len(got) >= 2)
}

func TestList_Empty(t *testing.T) {
	env := cogtest.New(t)
	var n int
	for _, err := range env.Providers.Groups.List() {
		require.NoError(t, err)
		n++
	}
	require.Equal(t, 0, n)
}

// --- Get / Update / Delete missing ---

func TestGet_Missing(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Groups.Get("does-not-exist")
	require.True(t, err != nil)
}

func TestUpdate_Missing(t *testing.T) {
	env := cogtest.New(t)
	err := env.Providers.Groups.Update("missing-id", "newname")
	require.True(t, err != nil)
}

func TestDelete_Missing(t *testing.T) {
	env := cogtest.New(t)
	err := env.Providers.Groups.Delete("missing-id")
	require.True(t, err != nil)
}

// --- Cycle detection ---

func TestAddMember_CycleRejected(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	owner := alice.ID

	gA, err := env.Providers.Groups.Create("A", nil, &owner)
	require.NoError(t, err)
	gB, err := env.Providers.Groups.Create("B", nil, &owner)
	require.NoError(t, err)

	// B becomes a member of A.
	require.NoError(t, env.Providers.Groups.AddMember(gA.ID, gB.ID, group.GroupMember))

	// Trying to add A to B would create a cycle.
	err = env.Providers.Groups.AddMember(gB.ID, gA.ID, group.GroupMember)
	require.True(t, err != nil)
}

func TestAddMember_SelfRejected(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("loop", nil, &owner)
	require.NoError(t, err)

	err = env.Providers.Groups.AddMember(g.ID, g.ID, group.GroupMember)
	require.True(t, err != nil)
}

// --- ListMembers ---

func TestListMembers(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("team", nil, &owner)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, bob.ID, group.User))

	var n int
	for m, err := range env.Providers.Groups.ListMembers(g.ID) {
		require.NoError(t, err)
		require.True(t, m.MemberID != "")
		n++
	}
	require.Equal(t, 2, n)
}

// --- ListClosureUserIDs (transitive membership) ---

func TestListClosureUserIDs_Direct(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("g", nil, &owner)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, bob.ID, group.User))

	users, err := env.Providers.Groups.ListClosureUserIDs(g.ID)
	require.NoError(t, err)
	require.Equal(t, 2, len(users))
}

func TestListClosureUserIDs_Nested(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")
	owner := alice.ID

	parent, err := env.Providers.Groups.Create("parent", nil, &owner)
	require.NoError(t, err)
	child, err := env.Providers.Groups.Create("child", nil, &owner)
	require.NoError(t, err)

	// parent contains child; child has bob+carol; alice in parent directly.
	require.NoError(t, env.Providers.Groups.AddMember(parent.ID, alice.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(parent.ID, child.ID, group.GroupMember))
	require.NoError(t, env.Providers.Groups.AddMember(child.ID, bob.ID, group.User))
	require.NoError(t, env.Providers.Groups.AddMember(child.ID, carol.ID, group.User))

	users, err := env.Providers.Groups.ListClosureUserIDs(parent.ID)
	require.NoError(t, err)
	require.Equal(t, 3, len(users)) // alice, bob, carol via closure
}

// --- ListOwnedByUser ---

func TestListOwnedByUser_FilterByOwner(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	aGrp, err := env.Providers.Groups.Create("a-grp", nil, &alice.ID)
	require.NoError(t, err)
	bGrp, err := env.Providers.Groups.Create("b-grp", nil, &bob.ID)
	require.NoError(t, err)

	// Ownership is recorded as a group_members row with member_type=Owner.
	require.NoError(t, env.Providers.Groups.AddMember(aGrp.ID, alice.ID, group.Owner))
	require.NoError(t, env.Providers.Groups.AddMember(bGrp.ID, bob.ID, group.Owner))

	var aliceGroups []group.Group
	for g, err := range env.Providers.Groups.ListOwnedByUser(alice.ID) {
		require.NoError(t, err)
		aliceGroups = append(aliceGroups, g)
	}
	require.Equal(t, 1, len(aliceGroups))
	require.Equal(t, "a-grp", aliceGroups[0].Name)
}

// --- RemoveMember idempotent + missing ---

func TestRemoveMember_NotInGroup(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("g", nil, &owner)
	require.NoError(t, err)

	// Removing a non-member is a no-op (no error).
	require.NoError(t, env.Providers.Groups.RemoveMember(g.ID, bob.ID))
}

// --- UpdateMemberType validation ---

func TestUpdateMemberType_BadType(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("g", nil, &owner)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.User))

	err = env.Providers.Groups.UpdateMemberType(g.ID, alice.ID, "bogus")
	require.True(t, err != nil)
}

// --- MemberType.IsValid ---

func TestMemberType_IsValid(t *testing.T) {
	require.True(t, group.MemberType(group.User).IsValid())
	require.True(t, group.MemberType(group.GroupMember).IsValid())
	require.False(t, group.MemberType("").IsValid())
	require.False(t, group.MemberType("admin").IsValid())
}

// --- Closure rebuild after manual mutation ---

func TestRebuildClosure_Idempotent(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	owner := alice.ID

	g, err := env.Providers.Groups.Create("g", nil, &owner)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, group.User))

	before, err := env.Providers.Groups.ListClosureUserIDs(g.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Groups.RebuildClosure())

	after, err := env.Providers.Groups.ListClosureUserIDs(g.ID)
	require.NoError(t, err)
	require.Equal(t, len(before), len(after))
}
