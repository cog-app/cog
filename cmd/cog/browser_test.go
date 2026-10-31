package main_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/dashboard"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
	"git.duckfam.us/jj-software/cog/plugin/notes"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

var allPlugins = []plugin.Plugin{
	dashboard.New(), files.New(), calendar.New(), contacts.New(),
	notes.New(), feed.New(), gallery.New(), bookmarks.New(), tasks.New(),
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
	env.CreateUser(t, "menu-open", "pass")
	env.CreateUser(t, "apps-menu", "pass")
	env.CreateUser(t, "searcher", "pass")
	filled := env.CreateUser(t, "filled", "pass")
	env.CreateUser(t, "storage", "pass")

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
		b.Click(t, `form[action="/settings/password"] button[type="submit"]`)
		b.WaitVisible(t, `input[name="current_password"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// The dropdown opens on focus; the subtest above only saw it closed.
	t.Run("UserDropdown/Open", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "menu-open", "pass")
		b.Click(t, ".navbar-end .dropdown-toggle")
		b.WaitVisible(t, "#user-menu")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	// At phone width with every plugin the nav collapses to the apps menu.
	// The menu is hidden at desktop width, so put the navbar into its
	// collapsed stage first and open the menu; the mobile resize then keeps
	// the stage the navbar script would have chosen anyway.
	t.Run("AppsMenu/Open", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "apps-menu", "pass")
		b.Eval(t, `() => document.querySelector(".navbar").classList.add("navbar--collapsed")`)
		b.Click(t, `[aria-controls="apps-menu"]`)
		b.WaitVisible(t, "#apps-menu")
		b.Snapshot(t, webtest.Mobile)
	})

	// On a phone the search box is behind a button and opens as a panel
	// under the navbar.
	t.Run("Search/Mobile", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "searcher", "pass")
		b.Eval(t, `() => document.querySelector(".navbar").classList.add("navbar--collapsed")`)
		b.Click(t, ".search-toggle")
		b.Type(t, "#search-box", "sec")
		b.WaitVisible(t, "#search-results .search-hit")
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Search/Pages", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "searcher", "pass")
		b.Type(t, "#search-box", "se")
		b.WaitVisible(t, "#search-results .search-hit")
		b.Snapshot(t)
	})

	t.Run("Search/NoResults", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "searcher", "pass")
		b.Type(t, "#search-box", "zzqqxx")
		b.WaitVisible(t, "#search-results .search-empty")
		b.Snapshot(t)
	})

	// The dashboard with something in it: every widget above is empty.
	t.Run("Dashboard/WithData", func(t *testing.T) {
		u := filled
		for _, title := range []string{"Groceries", "Meeting notes for the quarterly planning session with a long title"} {
			env.PostJSON(t, "/index.php/apps/notes/api/v1/notes",
				map[string]any{"title": title, "content": "body"}, u.Auth).Status(200)
		}
		env.PostJSON(t, "/index.php/apps/bookmarks/public/rest/v2/bookmark",
			map[string]any{"url": "https://example.com/a/very/long/path/that/keeps/going", "title": "Example bookmark"}, u.Auth).Status(200)
		env.Put(t, "/remote.php/dav/files/"+u.ID+"/report-2026-with-a-rather-long-file-name.txt",
			strings.NewReader("hello"), u.Auth).Status(201)
		grp, err := env.Providers.Groups.Create("family", nil, nil)
		require.NoError(t, err)
		require.NoError(t, env.Providers.Groups.AddMember(grp.ID, u.ID, group.Owner))

		b := eng.Start(t)
		b.Login(t, "filled", "pass")
		b.WaitVisible(t, ".app-grid")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Settings/Storage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "storage", "pass")
		b.Navigate(t, "/settings/storage")
		b.WaitVisible(t, `input[name="user_path"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

}

// The import page lists collections by database ID, so it gets an
// environment of its own: in the shared one the IDs depend on which other
// subtests ran first.
func TestBrowserSettingsData(t *testing.T) {
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "data", "pass")
	b := eng.Start(t)
	b.Login(t, "data", "pass")
	b.Navigate(t, "/settings/data")
	b.WaitVisible(t, "#import-file")
	b.Snapshot(t)
	b.Snapshot(t, webtest.Mobile)
}
