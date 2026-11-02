package user_test

import (
	"fmt"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func TestDelete(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	// Create a session and app password to verify cascade.
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	require.NotNil(t, sess)

	_, _, err = env.Providers.AppPasswords.Create(u.ID, "test-app")
	require.NoError(t, err)

	// Delete the user.
	require.NoError(t, env.Providers.Users.Delete(u.ID))

	// User should be gone.
	_, err = env.Providers.Users.GetByID(u.ID)
	require.Error(t, err)
	require.Equal(t, user.ErrNotFound, err)

	// Session should be gone (Validate returns nil, nil for missing sessions).
	validatedSess, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.True(t, validatedSess == nil, "expected session to be deleted")
}

func TestDeleteNotFound(t *testing.T) {
	env := cogtest.New(t)

	err := env.Providers.Users.Delete("nonexistent-id")
	require.Error(t, err)
	require.Equal(t, user.ErrNotFound, err)
}

func TestGetByIDs(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	got, err := env.Providers.Users.GetByIDs([]string{alice.ID, bob.ID, alice.ID, "", "missing"})
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
	require.Equal(t, "alice", got[alice.ID].Username)
	require.Equal(t, "bob", got[bob.ID].Username)

	empty, err := env.Providers.Users.GetByIDs(nil)
	require.NoError(t, err)
	require.Equal(t, 0, len(empty))
}

func TestGetByIDsChunks(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// More ids than fit in one query must still find the real one.
	ids := make([]string, 0, 1200)
	for i := range 1200 {
		ids = append(ids, fmt.Sprintf("missing-%d", i))
	}
	ids = append(ids, alice.ID)
	got, err := env.Providers.Users.GetByIDs(ids)
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	require.Equal(t, "alice", got[alice.ID].Username)
}

func TestGetByUsernames(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.CreateUser(t, "bob", "pass")

	got, err := env.Providers.Users.GetByUsernames([]string{"alice", "bob", "carol"})
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
	require.Equal(t, alice.ID, got["alice"].ID)
}
