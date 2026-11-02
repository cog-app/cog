package auth_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestEmailOTP_CreateAndValidate(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	code, err := env.Providers.EmailOTP.Create(u.ID)
	require.NoError(t, err)
	require.Equal(t, 6, len(code))

	// Wrong code rejected.
	ok, _ := env.Providers.EmailOTP.Validate(u.ID, "000000")
	require.False(t, ok)

	// Correct code accepted.
	ok, err = env.Providers.EmailOTP.Validate(u.ID, code)
	require.NoError(t, err)
	require.True(t, ok)

	// Replay rejected (marked used).
	ok, _ = env.Providers.EmailOTP.Validate(u.ID, code)
	require.False(t, ok)
}

func TestEmailOTP_CreateInvalidatesPrevious(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	first, err := env.Providers.EmailOTP.Create(u.ID)
	require.NoError(t, err)

	// New code wipes the previous unused code.
	second, err := env.Providers.EmailOTP.Create(u.ID)
	require.NoError(t, err)

	ok, _ := env.Providers.EmailOTP.Validate(u.ID, first)
	require.False(t, ok)

	ok, err = env.Providers.EmailOTP.Validate(u.ID, second)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestEmailOTP_Expired(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	code, err := env.Providers.EmailOTP.Create(u.ID)
	require.NoError(t, err)

	// Jump past the 10-minute expiry.
	env.Clock.Advance(11 * time.Minute)

	ok, _ := env.Providers.EmailOTP.Validate(u.ID, code)
	require.False(t, ok)
}

func TestEmailOTP_CrossUser(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	code, err := env.Providers.EmailOTP.Create(alice.ID)
	require.NoError(t, err)

	// Bob can't redeem Alice's code.
	ok, _ := env.Providers.EmailOTP.Validate(bob.ID, code)
	require.False(t, ok)
}

// TestEmailOTP_InvalidateFailureSurfaces checks that a new code is not
// handed out when the user's earlier codes could not be invalidated.
func TestEmailOTP_InvalidateFailureSurfaces(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, err := env.Providers.EmailOTP.Create(u.ID)
	require.NoError(t, err)

	_, err = env.Providers.DB.Exec(`CREATE TRIGGER no_otp_delete BEFORE DELETE ON email_otp BEGIN SELECT RAISE(ABORT, 'delete refused'); END`)
	require.NoError(t, err)

	_, err = env.Providers.EmailOTP.Create(u.ID)
	require.Error(t, err)

	var n int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT COUNT(*) FROM email_otp WHERE user_id = ?`, u.ID).Scan(&n))
	require.Equal(t, 1, n)
}
