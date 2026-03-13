package auth_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCreateAndValidateSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Equal(t, u.ID, sess.UserID)
	require.True(t, len(sess.ID) > 0)

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, sess.ID, got.ID)
	require.Equal(t, u.ID, got.UserID)
}

func TestValidateExpiredSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	// Manually expire.
	_, err = env.Providers.DB.Exec(`UPDATE sessions SET expires_at = 0 WHERE id = ?`, sess.ID)
	require.NoError(t, err)

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDeleteSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Sessions.Delete(sess.ID))

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
