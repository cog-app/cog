package web_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Login MFA flow ---

func TestMFALogin_ShowMFA_NoPendingCookie(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	// No mfa-pending cookie → redirect to /login.
	resp := env.Get(t, "/login/mfa", cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

func TestMFALogin_HandleMFA_GoodCode(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Enable TOTP via stores.
	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	_, err = env.Providers.TOTP.ConfirmSetup(alice.ID, gencode(t, key.Secret()))
	require.NoError(t, err)

	// Create a pending MFA token (simulating successful password step).
	pending, err := env.Providers.MFAPending.Create(alice.ID, "/")
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.MFAPendingCookieName+"="+pending.Token)

	// Submit a valid TOTP code.
	resp := env.PostForm(t, "/login/mfa", url.Values{
		"code": {gencode(t, key.Secret())},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	loc, _ := resp.Location()
	require.Equal(t, "/", loc.Path)

	// Pending cookie cleared (MaxAge=-1 in response). We don't inspect the
	// Set-Cookie directly here; instead verify the DB row was deleted.
	got, err := env.Providers.MFAPending.Validate(pending.Token)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestMFALogin_HandleMFA_BadCode(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	_, err = env.Providers.TOTP.ConfirmSetup(alice.ID, gencode(t, key.Secret()))
	require.NoError(t, err)

	pending, err := env.Providers.MFAPending.Create(alice.ID, "/")
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.MFAPendingCookieName+"="+pending.Token)

	resp := env.PostForm(t, "/login/mfa", url.Values{
		"code": {"000000"},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	loc, _ := resp.Location()
	require.Equal(t, "/login/mfa", loc.Path)

	// Pending token still valid; user can retry.
	got, _ := env.Providers.MFAPending.Validate(pending.Token)
	require.NotNil(t, got)
}

func TestMFALogin_HandleMFA_ExpiredPending(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	pending, err := env.Providers.MFAPending.Create(alice.ID, "/")
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.MFAPendingCookieName+"="+pending.Token)

	// Advance past 5-minute pending expiry.
	env.Clock.Advance(10 * time.Minute)

	resp := env.PostForm(t, "/login/mfa", url.Values{
		"code": {"000000"},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

// --- TOTP re-enroll requires current password (audit #8 / #16) ---

func TestTOTPReEnroll_RequiresCurrentPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Confirm TOTP once.
	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	_, err = env.Providers.TOTP.ConfirmSetup(alice.ID, gencode(t, key.Secret()))
	require.NoError(t, err)

	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Re-enroll WITHOUT current_password → bounced with flash error; secret
	// must still be the original.
	originalSecret := getStoredTOTPSecret(t, env, alice.ID)
	resp := env.PostForm(t, "/settings/totp/setup", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	require.Equal(t, originalSecret, getStoredTOTPSecret(t, env, alice.ID))

	// Wrong current_password → still no change.
	resp = env.PostForm(t, "/settings/totp/setup", url.Values{
		"csrf_token":       {token},
		"current_password": {"wrong"},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	require.Equal(t, originalSecret, getStoredTOTPSecret(t, env, alice.ID))

	// Correct current_password → secret changes.
	resp = env.PostForm(t, "/settings/totp/setup", url.Values{
		"csrf_token":       {token},
		"current_password": {"pass"},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	require.True(t, getStoredTOTPSecret(t, env, alice.ID) != originalSecret)
}

func TestTOTPReEnroll_FreshUser_NoPasswordNeeded(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// User without confirmed TOTP can call setup without a password.
	resp := env.PostForm(t, "/settings/totp/setup", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect)
	resp.Status(http.StatusSeeOther)
	require.True(t, getStoredTOTPSecret(t, env, alice.ID) != "")
}

// --- helpers ---

func gencode(t testing.TB, secret string) string {
	t.Helper()
	code, err := totp.GenerateCode(secret, time.Now())
	require.NoError(t, err)
	return code
}

func getStoredTOTPSecret(t *testing.T, env *cogtest.Env, userID string) string {
	t.Helper()
	var enc string
	err := env.Providers.DB.QueryRow(
		`SELECT totp_secret FROM users WHERE id = ?`, userID,
	).Scan(&enc)
	require.NoError(t, err)
	return enc
}
