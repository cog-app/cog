package auth_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestMFAPending_CreateValidateDelete(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	p, err := env.Providers.MFAPending.Create(u.ID, "/next")
	require.NoError(t, err)
	require.NotNil(t, p)
	require.Equal(t, u.ID, p.UserID)
	require.Equal(t, "/next", p.NextURL)
	require.True(t, len(p.Token) > 0)

	got, err := env.Providers.MFAPending.Validate(p.Token)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, u.ID, got.UserID)

	require.NoError(t, env.Providers.MFAPending.Delete(p.Token))

	got, err = env.Providers.MFAPending.Validate(p.Token)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestMFAPending_TokenHashedAtRest(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	p, err := env.Providers.MFAPending.Create(u.ID, "")
	require.NoError(t, err)

	// The plaintext token is NOT the stored value — the stored value is its
	// HMAC. A direct lookup by plaintext returns nothing.
	var raw string
	err = env.Providers.DB.QueryRow(
		`SELECT token FROM mfa_pending WHERE user_id = ?`, u.ID,
	).Scan(&raw)
	require.NoError(t, err)
	require.True(t, raw != p.Token)
	require.True(t, len(raw) == 64) // SHA256 hex
}

func TestMFAPending_Expired(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	p, err := env.Providers.MFAPending.Create(u.ID, "")
	require.NoError(t, err)

	env.Clock.Advance(6 * time.Minute) // pending duration is 5 min

	got, err := env.Providers.MFAPending.Validate(p.Token)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestMFAPending_UnknownToken(t *testing.T) {
	env := cogtest.New(t)
	got, err := env.Providers.MFAPending.Validate("not-a-real-token")
	require.NoError(t, err)
	require.Nil(t, got)
}
