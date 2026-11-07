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

// A client address blocked for wrong passwords keeps its working apps: an
// app password is checked even then, over Basic and Bearer alike, while a
// password, even the right one, is refused.
func TestBlockedAddressKeepsWorkingApps(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	token, _, err := env.Providers.AppPasswords.Create(alice.ID, "phone")
	require.NoError(t, err)
	for i := range 10 {
		require.Equal(t, "/login", signInLands(t, env, "alice", "guess"+string(rune('a'+i))))
	}
	require.Equal(t, "/login", signInLands(t, env, "alice", "pass"))
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+alice.ID+"/", "", cogtest.BasicAuth("alice", "pass"), cogtest.Header("Depth", "0")).Status(http.StatusUnauthorized)

	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+alice.ID+"/", "", cogtest.BasicAuth("alice", token), cogtest.Header("Depth", "0")).Status(http.StatusMultiStatus)
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", cogtest.Header("Authorization", "Bearer "+token), cogtest.Header("OCS-APIRequest", "true")).Status(http.StatusOK)

	// Another user's token under alice's name is still refused.
	bob := env.CreateUser(t, "bob", "pass")
	bobToken, _, err := env.Providers.AppPasswords.Create(bob.ID, "laptop")
	require.NoError(t, err)
	webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+alice.ID+"/", "", cogtest.BasicAuth("alice", bobToken), cogtest.Header("Depth", "0")).Status(http.StatusUnauthorized)

	// And a blocked address may still start signing an app in again.
	env.Post(t, "/index.php/login/v2").Status(http.StatusOK)
}

// Starting sign-in flows is capped on its own count, and doesn't use up the
// address's allowance for passwords.
func TestLoginFlowsDoNotBlockSignIn(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	for range 10 {
		env.Post(t, "/index.php/login/v2").Status(http.StatusOK)
	}
	env.Post(t, "/index.php/login/v2").Status(http.StatusTooManyRequests)
	require.True(t, signInLands(t, env, "alice", "pass") != "/login", "flows blocked the web sign-in")
}
