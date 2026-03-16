package user_test

import (
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
