package web_test

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

// Regression tests for the November 2026 security audit
// (docs/superpowers/audit/security-2026-11.md). Each names the finding it
// guards.

// C1: the default volume is the data directory. A user who is not an admin
// may not browse it, and so may not read other users' files through it.
func TestAuditSystemVolumeNotOpenToUsers(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass") // the first user is the admin
	bob := env.CreateUser(t, "bob", "pass")
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)

	bobCookie, _ := sessionAndCSRF(t, env, bob.ID)
	env.Get(t, "/volumes/"+vol.ID+"/browse", bobCookie).Status(http.StatusForbidden)
	env.Get(t, "/volumes/"+vol.ID+"/download?path=/admin/files/x", bobCookie).Status(http.StatusForbidden)

	adminCookie, _ := sessionAndCSRF(t, env, admin.ID)
	env.Get(t, "/volumes/"+vol.ID+"/browse", adminCookie).Status(http.StatusOK)

	ok, err := env.Providers.Access.CanAccess(bob.ID, vol.ID)
	require.NoError(t, err)
	require.False(t, ok)
}

// C2: a user may not mount the default volume, at any subpath.
func TestAuditCannotMountSystemVolume(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, bob.ID)
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)

	env.PostForm(t, "/settings/mounts", url.Values{
		"csrf_token":     {token},
		"volume_id":      {vol.ID},
		"user_path":      {"/everyone"},
		"volume_subpath": {""},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	for m, err := range env.Providers.Mounts.ListByUser(bob.ID) {
		require.NoError(t, err)
		require.True(t, m.UserPath != "/everyone", "bob mounted the data directory")
	}
}

// C2: a volume shared read-only mounts read-only.
func TestAuditReadOnlyShareMountsReadOnly(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")

	vol, err := env.Providers.Volumes.Create("bobs", "", "mem://", &bob.ID, "{}")
	require.NoError(t, err)
	_, err = env.Providers.Access.ShareVolume(bob.ID, vol.ID, "USER", carol.ID, 1)
	require.NoError(t, err)

	cookie, token := sessionAndCSRF(t, env, carol.ID)
	env.PostForm(t, "/settings/mounts", url.Values{
		"csrf_token": {token},
		"volume_id":  {vol.ID},
		"user_path":  {"/bobs"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	found := false
	for m, err := range env.Providers.Mounts.ListByUser(carol.ID) {
		require.NoError(t, err)
		if m.UserPath == "/bobs" {
			found = true
			require.True(t, m.ReadOnly, "a read-only share made a writable mount")
		}
	}
	require.True(t, found)
}

// C3: a user may not make a volume of a path on the server.
func TestAuditUserCannotCreateLocalVolume(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, bob.ID)

	env.PostForm(t, "/settings/volumes", url.Values{
		"csrf_token": {token},
		"name":       {"root"},
		"conn_type":  {"file"},
		"file_path":  {"/"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)

	_, err := env.Providers.Volumes.GetByName("root")
	require.True(t, errors.Is(err, volume.ErrNotFound), "got %v", err)

	// One that got into the database anyway does not open.
	v, err := env.Providers.Volumes.Create("legacy", "", "file:///", &bob.ID, "{}")
	require.NoError(t, err)
	_, err = volume.NewBackend(v)
	require.True(t, err != nil, "a user's local-path volume opened")
}

// C4: sharing something that is not yours is refused, and a share made of
// someone else's volume grants nothing.
func TestAuditCannotShareOthersVolume(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	mallory := env.CreateUser(t, "mallory", "pass")
	vol, err := env.Providers.Volumes.Create("bobs", "", "mem://", &bob.ID, "{}")
	require.NoError(t, err)

	cookie, token := sessionAndCSRF(t, env, mallory.ID)
	env.PostForm(t, "/api/shares/create", url.Values{
		"csrf_token":    {token},
		"resource_type": {volume.ShareResourceType},
		"resource_id":   {vol.ID},
		"subject_type":  {"USER"},
		"subject_id":    {mallory.ID},
	}, cookie).Status(http.StatusForbidden)
	env.PostForm(t, "/api/shares/public-link-form", url.Values{
		"csrf_token":    {token},
		"resource_type": {volume.ShareResourceType},
		"resource_id":   {vol.ID},
	}, cookie).Status(http.StatusForbidden)

	// A share written behind the handlers' back still counts for nothing.
	_, err = env.Providers.Shares.Create(mallory.ID, volume.ShareResourceType, vol.ID, "USER", mallory.ID, 3)
	require.NoError(t, err)
	ok, err := env.Providers.Access.CanAccess(mallory.ID, vol.ID)
	require.NoError(t, err)
	require.False(t, ok)
}

// H1: a user with MFA cannot sign in with just their password over Basic
// auth; an app password still works.
func TestAuditBasicAuthPasswordRefusedWithMFA(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	key, err := env.Providers.TOTP.BeginSetup(alice.ID, alice.Username, "Cog")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now()) // the totp package reads the real clock
	require.NoError(t, err)
	_, err = env.Providers.TOTP.ConfirmSetup(alice.ID, code)
	require.NoError(t, err)

	env.Get(t, "/ocs/v2.php/cloud/user?format=json", alice.Auth).Status(http.StatusUnauthorized)
	env.Get(t, "/settings/profile", alice.Auth, cogtest.Header("Accept", "text/html"), cogtest.StopRedirect).
		Status(http.StatusSeeOther)

	appPW, _, err := env.Providers.AppPasswords.Create(alice.ID, "client")
	require.NoError(t, err)
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", cogtest.BasicAuth("alice", appPW)).Status(http.StatusOK)
}

// H7: a user's HTML file is served sandboxed, so its scripts do not run as
// the site.
func TestAuditRawFileIsSandboxed(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/files/upload?path=/evil.html", "<script>alert(document.cookie)</script>", alice.Auth).
		Status(http.StatusCreated)
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	resp := env.Get(t, "/apps/files/evil.html?raw=1", cookie).Status(http.StatusOK)
	require.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))

	resp = env.Get(t, "/remote.php/dav/files/alice/evil.html", alice.Auth).Status(http.StatusOK)
	require.Equal(t, "sandbox", resp.Header.Get("Content-Security-Policy"))
}

// Every response refuses to be framed by another site.
func TestAuditFrameOptions(t *testing.T) {
	env := cogtest.New(t)
	resp := env.Get(t, "/login")
	require.Equal(t, "SAMEORIGIN", resp.Header.Get("X-Frame-Options"))
}

// safeNext: a backslash path is another host to a browser.
func TestAuditLoginNextRejectsBackslash(t *testing.T) {
	for _, next := range []string{`/\evil.example`, "//evil.example", "/\t/evil.example", "https://evil.example"} {
		require.False(t, auth.LocalPath(next), "%q accepted", next)
	}
	require.True(t, auth.LocalPath("/apps/files?x=1"))
}

// A login form another site submits is refused.
func TestAuditCrossSiteLoginRefused(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	env.PostForm(t, "/login", url.Values{"username": {"alice"}, "password": {"pass"}},
		cogtest.Header("Sec-Fetch-Site", "cross-site"), cogtest.StopRedirect).Status(http.StatusForbidden)
	env.PostForm(t, "/login", url.Values{"username": {"alice"}, "password": {"pass"}},
		cogtest.Header("Sec-Fetch-Site", "same-origin"), cogtest.StopRedirect).Status(http.StatusSeeOther)
}

// Changing the email address, where resets go, needs the password.
func TestAuditEmailChangeNeedsPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	env.PostForm(t, "/settings/profile", url.Values{
		"csrf_token": {token}, "display_name": {"A"}, "email": {"attacker@example.com"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	u, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "", u.Email)

	env.PostForm(t, "/settings/profile", url.Values{
		"csrf_token": {token}, "display_name": {"A"}, "email": {"alice@example.com"}, "current_password": {"pass"},
	}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	u, err = env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "alice@example.com", u.Email)
}

// An invitation from a user who is not an admin cannot open an account
// while registration is closed.
func TestAuditInviteRegistrationNeedsAdminInviter(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	g, err := env.Providers.Groups.Create("bobs friends", nil, &bob.ID)
	require.NoError(t, err)
	inv, err := env.Providers.Invitations.Create(g.ID, "", bob.ID, 24, "user")
	require.NoError(t, err)

	env.Get(t, "/invite/"+inv.Token).Status(http.StatusOK).NotContains(`name="password"`)
	env.PostForm(t, "/invite/"+inv.Token, url.Values{
		"invite_token": {inv.Token}, "username": {"sneaky"}, "password": {"pw"}, "confirm": {"pw"},
	}, cogtest.StopRedirect).Status(http.StatusSeeOther)
	_, err = env.Providers.Users.GetByUsername("sneaky")
	require.True(t, err != nil, "a non-admin's invitation opened an account")
}
