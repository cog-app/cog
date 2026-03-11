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

func TestBrowserMin(t *testing.T) {
	t.Parallel()
	env := cogtest.New(t, allPlugins...)
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "admin", "pass")
	b := eng.Start(t)
	browserLogin(t, b, "admin", "pass")
	url := b.URL(t)
	require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
}

func TestBrowserCore(t *testing.T) {
	t.Parallel()
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
		browserLogin(t, b, "login-ok", "pass")
		url := b.URL(t)
		require.True(t, !strings.Contains(url, "/login"), "expected redirect away from /login, got %s", url)
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
	})

	t.Run("Dashboard", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "dash", "pass")
		b.WaitVisible(t, ".app-grid")
		count := b.Count(t, ".app-card")
		require.True(t, count >= 8, "expected at least 8 app cards, got %d", count)
	})

	t.Run("Navigation", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "nav", "pass")
		b.WaitVisible(t, ".navbar-nav")
		count := b.Count(t, ".navbar-nav a")
		require.True(t, count >= 1, "expected nav links, got %d", count)
	})

	t.Run("UserDropdown", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "dropdown", "pass")
		b.WaitVisible(t, ".dropdown-toggle")
		// Verify the dropdown menu has links. Query the DOM directly
		// since the CSS dropdown toggle isn't reliably testable.
		n := b.Eval(t, `document.querySelectorAll('.dropdown-menu a').length`)
		count, _ := n.(float64)
		require.True(t, count >= 3, "expected at least 3 dropdown links, got %v", n)
	})

	t.Run("Settings/Profile", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "profile", "pass")
		b.Navigate(t, "/settings/profile")
		b.WaitVisible(t, `input[name="display_name"]`)
		b.Type(t, `input[name="display_name"]`, "Test User")
		b.Click(t, `main button[type="submit"]`)
		b.WaitVisible(t, `input[name="display_name"]`)
	})

	t.Run("Settings/Password", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "security", "pass")
		b.Navigate(t, "/settings/security")
		b.WaitVisible(t, `input[name="current_password"]`)
		b.Type(t, `input[name="current_password"]`, "pass")
		b.Type(t, `input[name="new_password"]`, "newpass123")
		b.Type(t, `input[name="confirm_password"]`, "newpass123")
		b.Click(t, `main button[type="submit"]`)
		b.WaitVisible(t, `input[name="current_password"]`)
	})
}

func TestBrowserPluginsCRUD(t *testing.T) {
	t.Parallel()
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
		b := eng.Start(t)
		browserLogin(t, b, "notes-empty", "pass")
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "No notes yet")
	})

	t.Run("Notes/Create", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "notes-create", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "My Test Note")
		b.Type(t, "#content", "Hello from browser test")
		b.Click(t, `main button[type="submit"]`)
		// POST redirects to the note detail view, not the list.
		b.WaitText(t, "My Test Note")
	})

	t.Run("Bookmarks/EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "bm-empty", "pass")
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "No bookmarks yet")
	})

	t.Run("Bookmarks/Create", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "bm-create", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Example Site")
		b.Type(t, "#tags", "test,browser")
		b.Click(t, `main button[type="submit"]`)
		// POST redirects to the bookmark detail view, not the list.
		b.WaitText(t, "Example Site")
	})

	t.Run("Tasks/CreateListAndTask", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "tasks-create", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "My List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.Type(t, `[data-testid="new-task-summary"]`, "First task")
		b.Click(t, `[data-testid="new-task-submit"]`)
		b.WaitVisible(t, `#task-tree .task-item input[type="checkbox"]`)
	})

	t.Run("Tasks/ToggleComplete", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "tasks-toggle", "pass")
		b.Navigate(t, "/apps/tasks/")
		b.Type(t, `[data-testid="new-list-name"]`, "Toggle List")
		b.Click(t, `[data-testid="new-list-submit"]`)
		b.Type(t, `[data-testid="new-task-summary"]`, "Toggle me")
		b.Click(t, `[data-testid="new-task-submit"]`)
		b.Click(t, `#task-tree .task-item input[type="checkbox"]`)
		b.WaitVisible(t, ".task-item.completed")
	})

	t.Run("Contacts/Create", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "contacts-create", "pass")
		b.Navigate(t, "/apps/contacts/")
		b.WaitText(t, "No contacts yet")
		// Open the new contact modal via JS onclick handler.
		b.Eval(t, `document.getElementById('create-contact-modal').style.display='flex'`)
		b.Type(t, "#cc-fn", "Alice Smith")
		b.Click(t, `#create-contact-modal button[type="submit"]`)
		b.WaitText(t, "Alice Smith")
	})
}

func TestBrowserPluginsNav(t *testing.T) {
	t.Parallel()
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
		b := eng.Start(t)
		browserLogin(t, b, "cal", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
	})

	t.Run("RSS/Settings", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "rss-settings", "pass")
		b.Navigate(t, "/apps/rss/settings")
		b.WaitVisible(t, "#url")
	})

	t.Run("RSS/EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "rss-empty", "pass")
		b.Navigate(t, "/apps/rss/")
		b.WaitVisible(t, "#rss-sidebar")
	})

	t.Run("Gallery/PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "gallery", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
	})

	t.Run("Gallery/Settings", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "gallery-settings", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.WaitText(t, "Gallery Settings")
	})

	t.Run("Files/PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "files", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
	})

	t.Run("Files/CreateFolder", func(t *testing.T) {
		b := eng.Start(t)
		browserLogin(t, b, "files-folder", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Test Folder")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitVisible(t, "#files-table")
		b.WaitText(t, "Test Folder")
	})
}
