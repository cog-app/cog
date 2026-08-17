package web_test

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func TestSettingsShowSecurity(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/security", cookie).Status(200)
}

func TestSettingsShowProfile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	env.Get(t, "/settings/profile", cookie).Status(200).Contains("alice")
}

func TestSettingsUpdateProfile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/profile", url.Values{
		"csrf_token":   {token},
		"display_name": {"Alice W"},
		"email":        {"alice@example.com"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "Alice W", u.DisplayName)
	require.Equal(t, "alice@example.com", u.Email)
}

func TestSettingsCreateMount(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	env.PostForm(t, "/settings/mounts", url.Values{
		"csrf_token":     {token},
		"volume_id":      {vol.ID},
		"user_path":      {"/extra"},
		"volume_subpath": {"extra"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify mount was created.
	var found bool
	for m, err := range env.Providers.Mounts.ListByUser(alice.ID) {
		require.NoError(t, err)
		if m.UserPath == "/extra" {
			found = true
		}
	}
	require.True(t, found, "mount at /extra should exist")
}

func TestSettingsDeleteMount(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	m, err := env.Providers.Mounts.Create(alice.ID, "/extra", vol.ID, "extra", false)
	require.NoError(t, err)

	env.Delete(t, "/settings/mounts/"+m.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Mounts.GetByID(m.ID)
	require.Error(t, err)
}

func TestSettingsDeleteRootMountFails(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	// Create a root mount manually (cogtest users don't get one by default).
	root, err := env.Providers.Mounts.Create(alice.ID, "/", vol.ID, alice.Username+"/files", false)
	require.NoError(t, err)

	env.Delete(t, "/settings/mounts/"+root.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(400)
}

func TestSettingsCreateUserVolume(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"my-remote"},
		"conn_type":  {"webdav"},
		"remote_url": {"https://dav.example.com/files/"},
		"auth_type":  {"basic"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	v, err := env.Providers.Volumes.GetByName("my-remote")
	require.NoError(t, err)
	require.NotNil(t, v.OwnerUserID)
	require.Equal(t, alice.ID, *v.OwnerUserID)
}

func TestSettingsDeleteUserVolume(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	v, err := env.Providers.Volumes.Create("alice-vol", "", "webdav://x", &alice.ID, "{}")
	require.NoError(t, err)

	env.Delete(t, "/settings/volumes/"+v.ID,
		cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)

	_, err = env.Providers.Volumes.GetByID(v.ID)
	require.Error(t, err)
}

func TestSettingsDeleteOtherUserVolumeForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	bobCookie, bobToken := sessionAndCSRF(t, env, bob.ID)

	v, err := env.Providers.Volumes.Create("alice-vol", "", "webdav://x", &alice.ID, "{}")
	require.NoError(t, err)

	env.Delete(t, "/settings/volumes/"+v.ID,
		bobCookie, cogtest.Header("X-CSRF-Token", bobToken)).Status(403)
}

func TestTOTPSetupConfirmAndDisable(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Setup TOTP directly via store (to get the secret for code generation).
	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)

	// Confirm TOTP via HTTP endpoint.
	env.PostForm(t, "/settings/totp/confirm", url.Values{
		"csrf_token": {token},
		"code":       {code},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify MFA is now enabled.
	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "totp", u.MFAMethod)

	// Disable TOTP via HTTP endpoint.
	env.PostForm(t, "/settings/totp/disable", url.Values{
		"csrf_token": {token},
		"password":   {"pass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err = env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "", u.MFAMethod)
}

func TestTOTPBackupCodes(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Setup and confirm TOTP directly via store.
	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	backupCodes, err := env.Providers.TOTP.ConfirmSetup(alice.ID, code)
	require.NoError(t, err)
	require.True(t, len(backupCodes) > 0)

	// Regenerate backup codes via HTTP endpoint.
	env.PostForm(t, "/settings/totp/backup-codes", url.Values{
		"csrf_token": {token},
		"password":   {"pass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify backup codes were regenerated.
	count, err := env.Providers.TOTP.BackupCodeCount(alice.ID)
	require.NoError(t, err)
	require.True(t, count > 0)
}

func TestEmailOTPEnableDisable(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Set email first (required for email OTP).
	err := env.Providers.Users.Update(alice.ID, user.UpdateOpts{Email: new("alice@example.com")})
	require.NoError(t, err)

	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Enable email OTP.
	env.PostForm(t, "/settings/email-otp/enable", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "email", u.MFAMethod)

	// Disable email OTP.
	env.PostForm(t, "/settings/email-otp/disable", url.Values{
		"csrf_token": {token},
		"password":   {"pass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	u, err = env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "", u.MFAMethod)
}

func TestEmailOTPRequiresEmail(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Try to enable without email set — should redirect with error.
	env.PostForm(t, "/settings/email-otp/enable", url.Values{
		"csrf_token": {token},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// MFA should still be empty.
	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "", u.MFAMethod)
}

func TestForgotPasswordFlow(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	err := env.Providers.Users.Update(alice.ID, user.UpdateOpts{Email: new("alice@example.com")})
	require.NoError(t, err)

	// Show forgot password page.
	env.Get(t, "/forgot-password").Status(200).Contains("Forgot")

	// Submit forgot password.
	env.PostForm(t, "/forgot-password", url.Values{
		"identifier": {"alice"},
	}, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify reset email was sent.
	require.True(t, env.Mailbox.Count() > 0, "expected password reset email")
	msg := env.Mailbox.Last()
	require.True(t, msg != nil)

	// Extract token from email body.
	// The email contains a URL like: http://localhost/reset-password?token=xxx
	body := msg.Body
	if body == "" {
		body = msg.HTMLBody
	}
	tokenStart := findSubstring(body, "token=")
	require.True(t, tokenStart >= 0, "expected token in email body")
	tokenStr := body[tokenStart+6:]
	// Trim at next non-token character.
	for i, c := range tokenStr {
		if c == '"' || c == '&' || c == '\n' || c == '\r' || c == ' ' || c == '<' {
			tokenStr = tokenStr[:i]
			break
		}
	}

	// Show reset password page.
	env.Get(t, "/reset-password?token="+tokenStr, cogtest.StopRedirect).Status(200)

	// Submit new password.
	env.PostForm(t, "/reset-password", url.Values{
		"token":    {tokenStr},
		"password": {"newpass"},
		"confirm":  {"newpass"},
	}, cogtest.StopRedirect).Status(http.StatusSeeOther)

	// Verify login with new password works.
	env.PostForm(t, "/login", url.Values{
		"username": {"alice"},
		"password": {"newpass"},
	}, cogtest.StopRedirect).Status(http.StatusSeeOther)
}

func TestShowResetPasswordInvalidToken(t *testing.T) {
	env := cogtest.New(t)

	// Invalid token should redirect to forgot-password.
	env.Get(t, "/reset-password?token=invalid", cogtest.StopRedirect).
		Status(http.StatusSeeOther)
}

func TestShowResetPasswordMissingToken(t *testing.T) {
	env := cogtest.New(t)

	// Missing token should redirect to login.
	env.Get(t, "/reset-password", cogtest.StopRedirect).
		Status(http.StatusSeeOther)
}

func findSubstring(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
