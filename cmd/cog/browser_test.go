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

func browserLogin(t testing.TB, b *webtest.Browser, user, pass string) {
	t.Helper()
	b.Navigate(t, "/login")
	b.Type(t, "#username", user)
	b.Type(t, "#password", pass)
	b.Click(t, `button[type="submit"]`)
	b.WaitNotVisible(t, "#password")
}

func TestBrowserCore(t *testing.T) {
	t.Skip("disabled while we figure out login and stuff")
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	// Pre-create all users before subtests to avoid DB contention.
	env.CreateUser(t, "login-ok", "pass")
	env.CreateUser(t, "login-bad", "pass")
	env.CreateUser(t, "dash", "pass")
	env.CreateUser(t, "nav", "pass")
	env.CreateUser(t, "dropdown", "pass")
	env.CreateUser(t, "profile", "pass")
	env.CreateUser(t, "security", "pass")

	t.Run("Login/Success", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "login-ok", "pass")
		url := b.URL(t)
		require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
	})

	t.Run("Login/BadPassword", func(t *testing.T) {
		b := eng.StartT(t)
		b.Navigate(t, "/login")
		b.Type(t, "#username", "login-bad")
		b.Type(t, "#password", "wrong")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, "#username")
		url := b.URL(t)
		require.True(t, strings.Contains(url, "/login"), "expected to stay on /login, got %s", url)
	})

	t.Run("Dashboard", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "dash", "pass")
		b.WaitVisible(t, ".app-grid")
		count := b.Count(t, ".app-card")
		require.True(t, count >= 8, "expected at least 8 app cards, got %d", count)
	})

	t.Run("Navigation", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "nav", "pass")
		b.WaitVisible(t, ".navbar-nav")
		count := b.Count(t, ".navbar-nav a")
		require.True(t, count >= 1, "expected nav links, got %d", count)
	})

	t.Run("UserDropdown", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "dropdown", "pass")
		b.WaitVisible(t, ".dropdown-toggle")
		b.Click(t, ".dropdown-toggle")
		b.WaitVisible(t, ".dropdown-menu")
	})

	t.Run("Settings/Profile", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "profile", "pass")
		b.Navigate(t, "/settings/profile")
		b.WaitVisible(t, `input[name="display_name"]`)
		b.Type(t, `input[name="display_name"]`, "Test User")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, `input[name="display_name"]`)
	})

	t.Run("Settings/Password", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "security", "pass")
		b.Navigate(t, "/settings/security")
		b.WaitVisible(t, `input[name="current_password"]`)
		b.Type(t, `input[name="current_password"]`, "pass")
		b.Type(t, `input[name="new_password"]`, "newpass123")
		b.Type(t, `input[name="confirm_password"]`, "newpass123")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, `input[name="current_password"]`)
	})
}

func TestBrowserPluginsCRUD(t *testing.T) {
	t.Skip("disabled while we get login figured out")
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "notes-empty", "pass")
	env.CreateUser(t, "notes-create", "pass")
	env.CreateUser(t, "bm-empty", "pass")
	env.CreateUser(t, "bm-create", "pass")
	env.CreateUser(t, "tasks-create", "pass")
	env.CreateUser(t, "tasks-toggle", "pass")
	env.CreateUser(t, "contacts-create", "pass")

	t.Run("Notes/EmptyState", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "notes-empty", "pass")
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "No notes yet")
	})

	t.Run("Notes/Create", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "notes-create", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "My Test Note")
		b.Type(t, "#content", "Hello from browser test")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, "#note-list")
		b.WaitText(t, "My Test Note")
	})

	t.Run("Bookmarks/EmptyState", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "bm-empty", "pass")
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "No bookmarks yet")
	})

	t.Run("Bookmarks/Create", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "bm-create", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Example Site")
		b.Type(t, "#tags", "test,browser")
		b.Click(t, `button[type="submit"]`)
		b.WaitVisible(t, "#bookmark-list")
		b.WaitText(t, "Example Site")
	})

	t.Run("Tasks/CreateListAndTask", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "tasks-create", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.WaitVisible(t, "#tasks-sidebar")
		b.Type(t, `#tasks-sidebar input[name="name"]`, "My List")
		b.Click(t, `#tasks-sidebar button[type="submit"]`)
		b.WaitVisible(t, `input[name="summary"]`)
		b.Type(t, `input[name="summary"]`, "First task")
		b.Click(t, `form button[type="submit"]`)
		b.WaitVisible(t, "#task-tree")
		b.WaitText(t, "First task")
	})

	t.Run("Tasks/ToggleComplete", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "tasks-toggle", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.WaitVisible(t, "#tasks-sidebar")
		b.Type(t, `#tasks-sidebar input[name="name"]`, "Toggle List")
		b.Click(t, `#tasks-sidebar button[type="submit"]`)
		b.WaitVisible(t, `input[name="summary"]`)
		b.Type(t, `input[name="summary"]`, "Toggle me")
		b.Click(t, `form button[type="submit"]`)
		b.WaitVisible(t, "#task-tree")
		b.WaitText(t, "Toggle me")
		b.Click(t, `.task-item input[type="checkbox"]`)
		b.WaitVisible(t, ".task-item.completed")
	})

	t.Run("Contacts/Create", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "contacts-create", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitVisible(t, "#contacts-list")
		b.ClickText(t, "New Contact")
		b.WaitVisible(t, "#create-contact-modal")
		b.Type(t, "#cc-fn", "Alice Smith")
		b.Click(t, `#create-contact-modal button[type="submit"]`)
		b.WaitVisible(t, "#contacts-list")
		b.WaitText(t, "Alice Smith")
	})
}

func TestBrowserPluginsNav(t *testing.T) {
	t.Skip("disabled while we get login figured out")
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "cal", "pass")
	env.CreateUser(t, "rss-settings", "pass")
	env.CreateUser(t, "rss-empty", "pass")
	env.CreateUser(t, "gallery", "pass")
	env.CreateUser(t, "gallery-settings", "pass")
	env.CreateUser(t, "files", "pass")
	env.CreateUser(t, "files-folder", "pass")

	t.Run("Calendar/PageLoads", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "cal", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
	})

	t.Run("RSS/Settings", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "rss-settings", "pass")
		b.Navigate(t, "/apps/rss/settings")
		b.WaitVisible(t, "#url")
	})

	t.Run("RSS/EmptyState", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "rss-empty", "pass")
		b.Navigate(t, "/apps/rss/")
		b.WaitVisible(t, "#rss-sidebar")
	})

	t.Run("Gallery/PageLoads", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "gallery", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
	})

	t.Run("Gallery/Settings", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "gallery-settings", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.WaitText(t, "Gallery Settings")
	})

	t.Run("Files/PageLoads", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "files", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
	})

	t.Run("Files/CreateFolder", func(t *testing.T) {
		b := eng.StartT(t)
		browserLogin(t, b, "files-folder", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		b.Type(t, `input[name="name"]`, "Test Folder")
		b.ClickText(t, "Create Folder")
		b.WaitVisible(t, "#files-table")
		b.WaitText(t, "Test Folder")
	})
}
