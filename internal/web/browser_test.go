package web_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

func TestBrowserLoginFlow(t *testing.T) {
	env := cogtest.New(t)
	_ = env.CreateUser(t, "alice", "secret")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.StartT(t)
	b.Navigate(t, "/login")

	// Fill in credentials and submit.
	b.Type(t, "#username", "alice")
	b.Type(t, "#password", "secret")
	b.Click(t, `button[type="submit"]`)

	// After login we should be redirected away from /login.
	b.WaitNotVisible(t, "#password")
	url := b.URL(t)
	require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
}

func TestBrowserLoginBadPassword(t *testing.T) {
	env := cogtest.New(t)
	_ = env.CreateUser(t, "alice", "secret")

	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	b := eng.StartT(t)
	b.Navigate(t, "/login")

	b.Type(t, "#username", "alice")
	b.Type(t, "#password", "wrong")
	b.Click(t, `button[type="submit"]`)

	// Should stay on login page with an error message.
	b.WaitVisible(t, "#username")
	url := b.URL(t)
	require.True(t, strings.Contains(url, "/login"), "expected to stay on /login, got %s", url)
}
