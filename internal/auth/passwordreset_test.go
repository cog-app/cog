package auth_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestPasswordReset_CreateValidateMarkUsed(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	token, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)
	require.True(t, len(token) > 0)

	userID, err := env.Providers.PasswordResets.Validate(token)
	require.NoError(t, err)
	require.Equal(t, u.ID, userID)

	// Mark used → Validate fails.
	require.NoError(t, env.Providers.PasswordResets.MarkUsed(token))
	_, err = env.Providers.PasswordResets.Validate(token)
	require.True(t, err != nil)
}

func TestPasswordReset_HashedAtRest(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	token, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)

	var stored string
	err = env.Providers.DB.QueryRow(
		`SELECT token FROM password_reset_tokens WHERE user_id = ?`, u.ID,
	).Scan(&stored)
	require.NoError(t, err)
	require.True(t, stored != token)
	require.True(t, len(stored) == 64) // SHA256 hex
}

func TestPasswordReset_CreateInvalidatesPrevious(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	first, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)

	second, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)

	_, err = env.Providers.PasswordResets.Validate(first)
	require.True(t, err != nil)

	userID, err := env.Providers.PasswordResets.Validate(second)
	require.NoError(t, err)
	require.Equal(t, u.ID, userID)
}

func TestPasswordReset_Expired(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	token, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)

	env.Clock.Advance(2 * time.Hour) // expiry is 1 hour

	_, err = env.Providers.PasswordResets.Validate(token)
	require.True(t, err != nil)
}

func TestPasswordReset_UnknownToken(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.PasswordResets.Validate("not-a-real-token")
	require.True(t, err != nil)
}
