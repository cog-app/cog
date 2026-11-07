package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// signInLands posts the login form and returns where it sends the browser:
// "/login" back to the form, anything else signed in.
func signInLands(t *testing.T, env *cogtest.Env, username, password string) string {
	t.Helper()
	resp := env.PostForm(t, "/login", url.Values{"username": {username}, "password": {password}}, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, err := resp.Location()
	require.NoError(t, err)
	return loc.Path
}

// A sync app still holding a revoked app password retries it for as long
// as it runs, from the same address as its owner's browser. Its retries
// are one refusal, not a guessing attack, and the owner can still sign in.
func TestStaleClientDoesNotLockOutItsOwner(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	token, ap, err := env.Providers.AppPasswords.Create(alice.ID, "phone")
	require.NoError(t, err)
	require.NoError(t, env.Providers.AppPasswords.Delete(ap.ID, alice.ID))

	for range 50 {
		webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+alice.ID+"/", "", cogtest.BasicAuth("alice", token), cogtest.Header("Depth", "0")).Status(http.StatusUnauthorized)
		env.Get(t, "/ocs/v2.php/cloud/user?format=json", cogtest.Header("Authorization", "Bearer "+token), cogtest.Header("OCS-APIRequest", "true")).Status(http.StatusUnauthorized)
	}
	require.True(t, signInLands(t, env, "alice", "pass") != "/login", "owner locked out by a stale client")
}

// Guessing still runs out: varied wrong passwords from one address are
// refused past the limit, even the right one.
func TestVariedGuessesStillLimited(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	for i := range 10 {
		require.Equal(t, "/login", signInLands(t, env, "alice", "guess"+string(rune('a'+i))))
	}
	require.Equal(t, "/login", signInLands(t, env, "alice", "pass"))
}

// Someone sure their password is P, refused for it, resets it to P: the
// refusal they had is forgotten, and P now signs them in.
func TestRefusalForgottenWhenPasswordSet(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "old-password")
	require.Equal(t, "/login", signInLands(t, env, "alice", "new-password"))
	require.NoError(t, env.Providers.Users.UpdatePassword(alice.ID, "new-password"))
	require.True(t, signInLands(t, env, "alice", "new-password") != "/login", "refusal outlived the password change")
}

// A name tried before its account existed works once the account does.
func TestRefusalForgottenWhenAccountMade(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass")
	require.Equal(t, "/login", signInLands(t, env, "carol", "carols-password"))
	env.CreateUser(t, "carol", "carols-password")
	require.True(t, signInLands(t, env, "carol", "carols-password") != "/login", "refusal outlived the account's creation")
}
