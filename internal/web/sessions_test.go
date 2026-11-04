package web_test

import (
	"net/http"
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestSecurityPage_ListsSessions(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	current, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	req, err := http.NewRequest("GET", "/", nil)
	require.NoError(t, err)
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0")
	req.RemoteAddr = "203.0.113.9:4000"
	other, err := env.Providers.Sessions.Start(alice.ID, req)
	require.NoError(t, err)

	body := env.Get(t, "/settings/security", cogtest.Header("Cookie", auth.SessionCookieName+"="+current.ID)).
		Status(http.StatusOK).
		Contains("Where you&#39;re signed in").
		Contains("Firefox on Linux").
		Contains("203.0.113.9").
		Contains("This device").
		Contains("/settings/sessions/" + other.Handle()).
		NotContains("/settings/sessions/" + current.Handle()).
		ReadBody()
	// The session ID is the cookie's bearer token; it must never reach the page.
	require.NotContains(t, body, current.ID)
	require.NotContains(t, body, other.ID)
}

func TestRevokeSession(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	current, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	other, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+current.ID)
	token := cogtest.Header("X-CSRF-Token", auth.CSRFToken(current.ID, env.Providers.CSRFSecret))

	env.Delete(t, "/settings/sessions/"+other.Handle(), cookie, token).Status(http.StatusOK)
	got, err := env.Providers.Sessions.Validate(other.ID)
	require.NoError(t, err)
	require.Nil(t, got)

	env.Delete(t, "/settings/sessions/"+other.Handle(), cookie, token).Status(http.StatusNotFound)
}

func TestRevokeOtherSessions(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	current, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	other, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)

	resp := env.PostForm(t, "/settings/sessions/revoke-others",
		url.Values{"csrf_token": {auth.CSRFToken(current.ID, env.Providers.CSRFSecret)}},
		cogtest.Header("Cookie", auth.SessionCookieName+"="+current.ID), cogtest.StopRedirect)
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)

	got, err := env.Providers.Sessions.Validate(other.ID)
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = env.Providers.Sessions.Validate(current.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
}
