package group_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCreateInvitation(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	inv, err := env.Providers.Invitations.Create(grp.ID, "user@example.com", owner.ID, 0, "")
	require.NoError(t, err)
	require.NotNil(t, inv)
	require.True(t, len(inv.Token) > 0)
	require.Equal(t, grp.ID, inv.GroupID)
	require.Equal(t, group.User, inv.MemberType) // default
	require.True(t, inv.ExpiresAt > inv.CreatedAt)
}

func TestGetByTokenValid(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	inv, err := env.Providers.Invitations.Create(grp.ID, "", owner.ID, 24, group.User)
	require.NoError(t, err)

	got, err := env.Providers.Invitations.GetByToken(inv.Token)
	require.NoError(t, err)
	require.Equal(t, inv.ID, got.ID)
}

func TestGetByTokenExpired(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	// Create with very short expiry, then manually expire it.
	inv, err := env.Providers.Invitations.Create(grp.ID, "", owner.ID, 1, group.User)
	require.NoError(t, err)

	// Manually set expires_at to the past.
	_, err = env.Providers.DB.Exec(`UPDATE group_invitations SET expires_at = 0 WHERE id = ?`, inv.ID)
	require.NoError(t, err)

	_, err = env.Providers.Invitations.GetByToken(inv.Token)
	require.Error(t, err)
	require.Equal(t, group.ErrNotFound, err)
}

func TestListByGroupFiltersExpired(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	// One valid.
	_, err = env.Providers.Invitations.Create(grp.ID, "a@b.com", owner.ID, 24, group.User)
	require.NoError(t, err)

	// One expired (manually expire).
	inv2, err := env.Providers.Invitations.Create(grp.ID, "c@d.com", owner.ID, 1, group.User)
	require.NoError(t, err)
	_, err = env.Providers.DB.Exec(`UPDATE group_invitations SET expires_at = 0 WHERE id = ?`, inv2.ID)
	require.NoError(t, err)

	var invitations []group.Invitation
	for inv, err := range env.Providers.Invitations.ListByGroup(grp.ID) {
		require.NoError(t, err)
		invitations = append(invitations, inv)
	}
	require.Equal(t, 1, len(invitations))
}

func TestDeleteExpired(t *testing.T) {
	env := cogtest.New(t)
	owner := env.CreateUser(t, "owner", "pass")
	grp, err := env.Providers.Groups.Create("devs", nil, &owner.ID)
	require.NoError(t, err)

	inv, err := env.Providers.Invitations.Create(grp.ID, "", owner.ID, 1, group.User)
	require.NoError(t, err)

	// Expire it.
	_, err = env.Providers.DB.Exec(`UPDATE group_invitations SET expires_at = 0 WHERE id = ?`, inv.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Invitations.DeleteExpired())

	// Should be gone even if we query directly.
	var count int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT COUNT(*) FROM group_invitations WHERE id = ?`, inv.ID).Scan(&count))
	require.Equal(t, 0, count)
}
