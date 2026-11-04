package auth_test

import (
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// otpCode returns a fresh TOTP code for the given base32 secret at real time.
// pquerna/otp validates against time.Now() (no injection point), so we must
// generate against the same clock.
func otpCode(t testing.TB, _ *cogtest.Env, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	return code
}

func TestTOTP_BeginSetupConfirmValidate(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	key, err := env.Providers.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	require.NoError(t, err)
	require.NotNil(t, key)
	secret := key.Secret()
	require.True(t, secret != "")

	// Before ConfirmSetup, Validate against a real code still works (decrypts secret),
	// but mfa_method isn't set so login flow won't redirect — that's a handler concern.
	first := otpCode(t, env, secret)
	ok, err := env.Providers.TOTP.Validate(u.ID, first)
	require.NoError(t, err)
	require.True(t, ok)

	// Confirm with the real code → MFA enabled + returns 10 backup codes.
	codes, err := env.Providers.TOTP.ConfirmSetup(u.ID, otpCode(t, env, secret))
	require.NoError(t, err)
	require.Equal(t, 10, len(codes))

	// Wrong code rejected.
	_, err = env.Providers.TOTP.ConfirmSetup(u.ID, "000000")
	require.True(t, err != nil)

	// A code is good once: the one Validate already accepted is refused.
	ok, err = env.Providers.TOTP.Validate(u.ID, first)
	require.NoError(t, err)
	require.False(t, ok)

	// Validate succeeds with the next step's code, which the allowed
	// clock skew accepts now.
	next, err := totp.GenerateCode(secret, time.Now().Add(30*time.Second))
	require.NoError(t, err)
	ok, err = env.Providers.TOTP.Validate(u.ID, next)
	require.NoError(t, err)
	require.True(t, ok)

	// Wrong code rejected (and no backup code matched either).
	ok, _ = env.Providers.TOTP.Validate(u.ID, "000000")
	require.False(t, ok)
}

func TestTOTP_BackupCodes(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	key, err := env.Providers.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	require.NoError(t, err)
	codes, err := env.Providers.TOTP.ConfirmSetup(u.ID, otpCode(t, env, key.Secret()))
	require.NoError(t, err)
	require.Equal(t, 10, len(codes))

	// Count starts at 10.
	count, err := env.Providers.TOTP.BackupCodeCount(u.ID)
	require.NoError(t, err)
	require.Equal(t, 10, count)

	// Use one.
	ok, err := env.Providers.TOTP.Validate(u.ID, codes[0])
	require.NoError(t, err)
	require.True(t, ok)

	count, _ = env.Providers.TOTP.BackupCodeCount(u.ID)
	require.Equal(t, 9, count)

	// Same code is now invalid (marked used).
	ok, _ = env.Providers.TOTP.Validate(u.ID, codes[0])
	require.False(t, ok)

	// RegenerateBackupCodes wipes and creates 10 new ones; old ones are dead.
	newCodes, err := env.Providers.TOTP.RegenerateBackupCodes(u.ID)
	require.NoError(t, err)
	require.Equal(t, 10, len(newCodes))

	count, _ = env.Providers.TOTP.BackupCodeCount(u.ID)
	require.Equal(t, 10, count)

	// Old code (#1) no longer works.
	ok, _ = env.Providers.TOTP.Validate(u.ID, codes[1])
	require.False(t, ok)

	// New code does.
	ok, err = env.Providers.TOTP.Validate(u.ID, newCodes[3])
	require.NoError(t, err)
	require.True(t, ok)
}

func TestTOTP_Disable(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	key, err := env.Providers.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	require.NoError(t, err)
	_, err = env.Providers.TOTP.ConfirmSetup(u.ID, otpCode(t, env, key.Secret()))
	require.NoError(t, err)

	require.NoError(t, env.Providers.TOTP.Disable(u.ID))

	// Validate fails — no secret stored.
	ok, _ := env.Providers.TOTP.Validate(u.ID, otpCode(t, env, key.Secret()))
	require.False(t, ok)

	// Backup codes wiped.
	count, _ := env.Providers.TOTP.BackupCodeCount(u.ID)
	require.Equal(t, 0, count)
}

// TestTOTP_KeyDerivation guards the audit's "CSRF secret reused as TOTP key"
// finding: a store built with a different master secret cannot decrypt the
// stored TOTP seed (derived keys diverge).
func TestTOTP_KeyDerivation(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	key, err := env.Providers.TOTP.BeginSetup(u.ID, u.Username, "Cog")
	require.NoError(t, err)

	ok, err := env.Providers.TOTP.Validate(u.ID, otpCode(t, env, key.Secret()))
	require.NoError(t, err)
	require.True(t, ok)

	// Build an independent TOTPStore on the same DB with a different master.
	other := auth.NewTOTPStore(env.Providers.DB, []byte("an-entirely-different-master-secret-32b"))
	ok, _ = other.Validate(u.ID, otpCode(t, env, key.Secret()))
	require.False(t, ok)
}
