package main_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/plugin/notes"
	"git.duckfam.us/jj-software/cog/plugin/rss"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

var allPlugins = []plugin.Plugin{
	dashboard.New(), files.New(), calendar.New(), contacts.New(),
	notes.New(), rss.New(), gallery.New(), bookmarks.New(), tasks.New(),
}

func TestBrowserMin(t *testing.T) {
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "admin", "pass")
	b := eng.Start(t)
	b.Login(t, "admin", "pass")
	url := b.URL(t)
	require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}

func TestBrowserCore(t *testing.T) {
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "login-ok", "pass")
	env.CreateUser(t, "login-bad", "pass")
	env.CreateUser(t, "dash", "pass")
	env.CreateUser(t, "nav", "pass")
	env.CreateUser(t, "dropdown", "pass")
	env.CreateUser(t, "profile", "pass")
	env.CreateUser(t, "security", "pass")

	t.Run("Login/Success", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "login-ok", "pass")
		url := b.URL(t)
		require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Login/BadPassword", func(t *testing.T) {
		b := eng.Start(t)
		b.Navigate(t, "/login")
		b.Type(t, "#username", "login-bad")
		b.Type(t, "#password", "wrong")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, "#username")
		url := b.URL(t)
		require.True(t, strings.Contains(url, "/login"), "expected to stay on /login, got %s", url)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Dashboard", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "dash", "pass")
		b.WaitVisible(t, ".app-grid")
		count := b.Count(t, ".app-card")
		require.True(t, count >= 8, "expected at least 8 app cards, got %d", count)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Navigation", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "nav", "pass")
		b.WaitVisible(t, ".navbar-nav")
		count := b.Count(t, ".navbar-nav a")
		require.True(t, count >= 1, "expected nav links, got %d", count)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("UserDropdown", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "dropdown", "pass")
		b.WaitVisible(t, ".dropdown-toggle")
		n := b.Eval(t, `document.querySelectorAll('.dropdown-menu a').length`)
		count, _ := n.(float64)
		require.True(t, count >= 3, "expected at least 3 dropdown links, got %v", n)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Settings/Profile", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "profile", "pass")
		b.Navigate(t, "/settings/profile")
		b.WaitVisible(t, `input[name="display_name"]`)
		b.Type(t, `input[name="display_name"]`, "Test User")
		b.Click(t, `main button[type="submit"]`)
		b.WaitVisible(t, `input[name="display_name"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Settings/Password", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "security", "pass")
		b.Navigate(t, "/settings/security")
		b.WaitVisible(t, `input[name="current_password"]`)
		b.Type(t, `input[name="current_password"]`, "pass")
		b.Type(t, `input[name="new_password"]`, "newpass123")
		b.Type(t, `input[name="confirm_password"]`, "newpass123")
		b.Click(t, `main button[type="submit"]`)
		b.WaitVisible(t, `input[name="current_password"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
