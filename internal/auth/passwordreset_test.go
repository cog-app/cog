package auth_test

import (
	"errors"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
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
	require.True(t, errors.Is(err, auth.ErrInvalidResetToken), "got %v", err)
}

// TestPasswordReset_InvalidSentinel checks that every way a link can be dead
// reports ErrInvalidResetToken, and a failure to check one does not.
func TestPasswordReset_InvalidSentinel(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	resets := env.Providers.PasswordResets

	used, err := resets.Create(u.ID)
	require.NoError(t, err)
	require.NoError(t, resets.MarkUsed(used))
	_, err = resets.Validate(used)
	require.True(t, errors.Is(err, auth.ErrInvalidResetToken), "used: got %v", err)

	expired, err := resets.Create(u.ID)
	require.NoError(t, err)
	env.Clock.Advance(2 * time.Hour)
	_, err = resets.Validate(expired)
	require.True(t, errors.Is(err, auth.ErrInvalidResetToken), "expired: got %v", err)

	_, err = env.Providers.DB.Exec(`ALTER TABLE password_reset_tokens RENAME TO password_reset_tokens_gone`)
	require.NoError(t, err)
	_, err = resets.Validate(expired)
	require.Error(t, err)
	require.False(t, errors.Is(err, auth.ErrInvalidResetToken), "a database error must not read as a bad link: %v", err)
}

// TestPasswordReset_InvalidateFailureSurfaces checks that a new token is not
// handed out when the user's earlier ones could not be invalidated.
func TestPasswordReset_InvalidateFailureSurfaces(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	first, err := env.Providers.PasswordResets.Create(u.ID)
	require.NoError(t, err)

	_, err = env.Providers.DB.Exec(`CREATE TRIGGER no_reset_delete BEFORE DELETE ON password_reset_tokens BEGIN SELECT RAISE(ABORT, 'delete refused'); END`)
	require.NoError(t, err)

	_, err = env.Providers.PasswordResets.Create(u.ID)
	require.Error(t, err)

	var n int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT COUNT(*) FROM password_reset_tokens WHERE user_id = ?`, u.ID).Scan(&n))
	require.Equal(t, 1, n, "no second token may exist beside the one that could not be invalidated")
	_, err = env.Providers.PasswordResets.Validate(first)
	require.NoError(t, err)
}
