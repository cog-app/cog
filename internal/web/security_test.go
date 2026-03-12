package web_test

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Authentication ---

func TestUnauthenticatedAPIReturns401(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	for _, p := range []string{
		"/ocs/v2.php/cloud/user?format=json",
		"/remote.php/dav/files/doesntmatter/",
	} {
		env.Get(t, p).Status(401)
	}
}

func TestUnauthenticatedWebRedirectsToLogin(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/settings/profile", cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

func TestBadPasswordReturns401(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	env.Get(t, "/ocs/v2.php/cloud/user?format=json",
		cogtest.BasicAuth("alice", "wrong")).Status(401)
}

func TestNonexistentUserReturns401(t *testing.T) {
	env := cogtest.New(t)

	env.Get(t, "/ocs/v2.php/cloud/user?format=json",
		cogtest.BasicAuth("ghost", "pass")).Status(401)
}

func TestBasicAuthWorks(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	var resp struct {
		OCS struct {
			Data struct{ ID string } `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", alice.Auth).
		Status(200).JSON(&resp)
	require.Equal(t, "alice", resp.OCS.Data.ID)
}

// --- App Passwords ---

func TestAppPasswordBasicAuth(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	plaintext, _, err := env.Providers.AppPasswords.Create(alice.ID, "test-app")
	require.NoError(t, err)

	var resp struct {
		OCS struct {
			Data struct{ ID string } `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/cloud/user?format=json",
		cogtest.BasicAuth("alice", plaintext)).
		Status(200).JSON(&resp)
	require.Equal(t, "alice", resp.OCS.Data.ID)
}

func TestAppPasswordBearerAuth(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	plaintext, _, err := env.Providers.AppPasswords.Create(alice.ID, "test-app")
	require.NoError(t, err)

	var resp struct {
		OCS struct {
			Data struct{ ID string } `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/cloud/user?format=json",
		cogtest.Auth("Bearer "+plaintext)).
		Status(200).JSON(&resp)
	require.Equal(t, "alice", resp.OCS.Data.ID)
}

func TestInvalidAppPasswordFails(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	env.Get(t, "/ocs/v2.php/cloud/user?format=json",
		cogtest.BasicAuth("alice", "not-a-real-app-password")).Status(401)
}

// --- Session Auth ---

func TestSessionCookieAuth(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)

	env.Get(t, "/settings/profile",
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)).
		Status(200).Contains("alice")
}

func TestExpiredSessionRedirects(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	env.Providers.Sessions.Delete(sess.ID)

	resp := env.Get(t, "/settings/profile",
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID),
		cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

func TestInvalidSessionTokenRedirects(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/settings/profile",
		cogtest.Header("Cookie", auth.SessionCookieName+"=bogus-token"),
		cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

// --- CSRF ---

func sessionAndCSRF(t *testing.T, env *cogtest.Env, userID string) (cookie cogtest.HTTPOpt, token string) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(userID)
	require.NoError(t, err)
	cookie = cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	token = auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	return
}

func TestCSRFRequiredOnPost(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	// POST without CSRF token should be rejected.
	resp := env.PostForm(t, "/settings/profile",
		url.Values{"display_name": {"Alice"}},
		cookie, cogtest.StopRedirect)
	// CSRF failure for web forms redirects (303) back to referer/current page.
	require.True(t, resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusSeeOther)
}

func TestCSRFWithValidToken(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// POST with valid CSRF token should succeed (PRG redirect).
	resp := env.PostForm(t, "/settings/profile",
		url.Values{"display_name": {"Alice"}, "csrf_token": {token}},
		cookie, cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	// Verify the update took effect.
	updated, err := env.Providers.Users.GetByID(alice.ID)
	require.NoError(t, err)
	require.Equal(t, "Alice", updated.DisplayName)
}

func TestCSRFWrongTokenRejected(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)

	resp := env.PostForm(t, "/settings/profile",
		url.Values{"display_name": {"Alice"}, "csrf_token": {"bad-token"}},
		cookie, cogtest.StopRedirect)
	require.True(t, resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusSeeOther)

	// Verify the update did NOT take effect.
	u, _ := env.Providers.Users.GetByID(alice.ID)
	require.Equal(t, "", u.DisplayName)
}

func TestCSRFViaHeader(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	resp := env.PostForm(t, "/settings/profile",
		url.Values{"display_name": {"Alice"}},
		cookie, cogtest.Header("X-CSRF-Token", token), cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
}

func TestCSRFNotRequiredForBasicAuth(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// API endpoints using Basic auth don't need CSRF.
	// 400 = reached the handler (not blocked by CSRF).
	resp := env.PostJSON(t, "/ocs/v2.php/apps/dav/api/v1/direct",
		map[string]any{"fileId": 999}, alice.Auth)
	require.True(t, resp.StatusCode != http.StatusForbidden)
}

// --- Authorization (admin) ---

func TestNonAdminCannotAccessAdmin(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "pass") // first user = admin
	bob := env.CreateUser(t, "bob", "pass")

	for _, p := range []string{"/admin/users", "/admin/settings", "/admin/groups", "/admin/volumes"} {
		env.Get(t, p, bob.Auth).Status(403)
	}
}

func TestAdminCanAccessAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "admin", "pass")

	for _, p := range []string{"/admin/settings", "/admin/users", "/admin/groups"} {
		env.Get(t, p, admin.Auth).Status(200)
	}
}

// --- Authorization (groups) ---

func TestGroupNonMemberForbidden(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	g, err := env.Providers.Groups.Create("secret", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, "OWNER"))

	env.Get(t, "/settings/groups/"+g.ID, bob.Auth).Status(403)
}

func TestGroupMemberCanView(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	g, err := env.Providers.Groups.Create("team", nil, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, alice.ID, "OWNER"))
	require.NoError(t, env.Providers.Groups.AddMember(g.ID, bob.ID, "USER"))

	env.Get(t, "/settings/groups/"+g.ID, bob.Auth).Status(200)
}

// --- Login form ---

func TestLoginSuccess(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	jar, _ := cookiejar.New(nil)
	env.Client.Jar = jar
	defer func() { env.Client.Jar = nil }()

	// Submit login form — follows redirect to home page.
	env.PostForm(t, "/login",
		url.Values{"username": {"alice"}, "password": {"pass"}}).
		Status(200)
}

func TestLoginFailure(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.PostForm(t, "/login",
		url.Values{"username": {"alice"}, "password": {"wrong"}},
		cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	loc, _ := resp.Location()
	require.Equal(t, "/login", loc.Path)
}

// --- Public endpoints don't require auth ---

func TestPublicEndpoints(t *testing.T) {
	env := cogtest.New(t)

	env.Get(t, "/status.php").Status(200)
	env.Get(t, "/ocs/v2.php/cloud/capabilities?format=json").Status(200)
	env.Get(t, "/index.php/204").Status(204)
}
