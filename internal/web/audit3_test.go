package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// Regression tests for the third audit (TODO.md, 2026-11-06).

// Starting a login flow needs no credentials and writes a row, so it counts
// against the client like a registration does.
func TestLoginFlowInitIsRateLimited(t *testing.T) {
	env := cogtest.New(t)
	limited := false
	for range 12 {
		resp := env.Post(t, "/index.php/login/v2")
		if resp.StatusCode == http.StatusTooManyRequests {
			limited = true
			break
		}
		resp.Status(200)
	}
	require.True(t, limited, "twelve flows started with no limit")
}

// Required MFA leaves open only what setting it up needs. Other settings
// pages wait like the rest, and a sync client with Basic auth is not sent
// to an HTML page.
func TestMFARequirementCoversSettingsButNotAPIClients(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	require.NoError(t, env.Providers.Settings.Set(settings.KeyMFARequired, "all"))
	cookie, _ := sessionAndCSRF(t, env, bob.ID)

	env.Get(t, "/settings/security", cookie).Status(http.StatusOK)
	env.Get(t, "/settings/storage", cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	env.Get(t, "/settings/profile", cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+bob.ID+"/", "", cogtest.BasicAuth("bob", "pass"), cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
}

// A mount point is a folder inside the user's files: not the root, which
// is their home, and not the trash.
func TestMountPointMustBeInsideFiles(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)
	vol, err := env.Providers.Volumes.GetByName("default")
	require.NoError(t, err)

	for _, userPath := range []string{"/", "//", "/.cog-trash", "/.cog-trash/x"} {
		env.PostForm(t, "/settings/mounts", url.Values{
			"csrf_token":     {token},
			"volume_id":      {vol.ID},
			"user_path":      {userPath},
			"volume_subpath": {"extra"},
		}, cookie, cogtest.StopRedirect).Status(http.StatusSeeOther)
	}
	for m, err := range env.Providers.Mounts.ListByUser(alice.ID) {
		require.NoError(t, err)
		require.Equal(t, "/", m.UserPath)
	}
}

// A share expiring on a day lasts through that day.
func TestOCSShareExpiresAtEndOfDay(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Put(t, "/remote.php/dav/files/"+alice.ID+"/x.txt", nil, alice.Auth)

	env.PostForm(t, "/ocs/v2.php/apps/files_sharing/api/v1/shares?format=json", url.Values{
		"path": {"/x.txt"}, "shareType": {"3"}, "expireDate": {"2030-01-01"},
	}, alice.Auth).Status(200)

	for rec, err := range env.Providers.Shares.ListByOwner(alice.ID) {
		require.NoError(t, err)
		require.True(t, rec.Expiration != nil)
		require.Equal(t, int64(1893542400), *rec.Expiration) // 2030-01-02T00:00:00Z
	}
}

// A WebDAV href is a URL: a name with a space, a hash or a percent sign is
// percent-encoded, or a client addresses the wrong file.
func TestWebDAVHrefsArePercentEncoded(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID
	env.Put(t, prefix+"/a%20b%23c%2525.txt", nil, alice.Auth).Status(http.StatusCreated)

	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "", alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(http.StatusMultiStatus)
	resp.Contains(prefix + "/a%20b%23c%2525.txt")
}

// The admin's password policy holds everywhere a password is set: at
// registration and on a change, with the reason shown to the user.
func TestPasswordPolicyEnforced(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyPublicRegistration, true))
	require.NoError(t, env.Providers.Settings.Set(settings.KeyPasswordMinLength, "12"))

	env.PostForm(t, "/register", url.Values{
		"username": {"bob"}, "password": {"short"}, "confirm_password": {"short"},
	}, cogtest.StopRedirect).Status(http.StatusSeeOther)
	_, err := env.Providers.Users.GetByUsername("bob")
	require.True(t, err != nil, "an account was made with a refused password")

	err = env.Providers.Users.UpdatePassword(alice.ID, "short")
	require.True(t, err != nil, "a refused password was set")
	require.NoError(t, env.Providers.Users.UpdatePassword(alice.ID, "long enough now"))
}
