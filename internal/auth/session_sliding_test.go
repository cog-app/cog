package auth_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func sessionCookie(resp *cogtest.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == auth.SessionCookieName {
			return c
		}
	}
	return nil
}

// TestSession_SlidesWithUse verifies that a session in use outlives its
// original 30 days, and that each renewal reissues a persistent cookie.
func TestSession_SlidesWithUse(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)

	// Within the renew interval nothing is written or reissued.
	resp := env.Get(t, "/settings/profile", cookie).Status(http.StatusOK)
	require.Nil(t, sessionCookie(resp))

	// Use it every 20 days for 100 days.
	for range 5 {
		env.Clock.Advance(20 * 24 * time.Hour)
		resp := env.Get(t, "/settings/profile", cookie).Status(http.StatusOK)
		ck := sessionCookie(resp)
		require.NotNil(t, ck)
		require.Equal(t, sess.ID, ck.Value)
		require.Equal(t, 30*24*60*60, ck.MaxAge)
	}

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, env.Now().Unix(), got.LastSeenAt.Unix())
}

// TestSession_ExpiresWhenIdle verifies that an unused session still expires.
func TestSession_ExpiresWhenIdle(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	env.Clock.Advance(31 * 24 * time.Hour)
	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestSession_LoginCookiePersists verifies the login cookie carries an
// expiry, so it survives the browser closing.
func TestSession_LoginCookiePersists(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.PostForm(t, "/login", map[string][]string{"username": {"alice"}, "password": {"pass"}},
		cogtest.StopRedirect, cogtest.Header("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0"))
	require.Equal(t, http.StatusSeeOther, resp.StatusCode)
	ck := sessionCookie(resp)
	require.NotNil(t, ck)
	require.Equal(t, 30*24*60*60, ck.MaxAge)

	got, err := env.Providers.Sessions.Validate(ck.Value)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "Mozilla/5.0 (X11; Linux x86_64) Firefox/140.0", got.UserAgent)
	require.True(t, got.IP != "", "session should record the client address")
}

// TestSession_HeaderAuthOnlyStartsSessionForPages verifies that sync and API
// clients authenticating per request do not leave a session row each time,
// while a browser using Basic Auth still gets one for its forms.
func TestSession_HeaderAuthOnlyStartsSessionForPages(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	for range 3 {
		resp := env.Get(t, "/settings/profile", u.Auth)
		require.Nil(t, sessionCookie(resp))
	}
	sessions, err := env.Providers.Sessions.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(sessions))

	resp := env.Get(t, "/settings/profile", u.Auth, cogtest.Header("Accept", "text/html")).Status(http.StatusOK)
	require.NotNil(t, sessionCookie(resp))
	sessions, err = env.Providers.Sessions.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(sessions))
}

// TestSession_CreatePrunesExpired verifies expired rows are dropped when the
// user next signs in.
func TestSession_CreatePrunesExpired(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	old, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	env.Clock.Advance(31 * 24 * time.Hour)
	_, err = env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	var n int
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE id = ?`, old.ID).Scan(&n))
	require.Equal(t, 0, n)
}

// TestSession_DeleteByHandle verifies a session is revoked by its handle and
// only by its owner.
func TestSession_DeleteByHandle(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)

	require.True(t, errors.Is(env.Providers.Sessions.DeleteByHandle(bob.ID, sess.Handle()), auth.ErrSessionNotFound))
	require.NoError(t, env.Providers.Sessions.DeleteByHandle(alice.ID, sess.Handle()))
	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}
