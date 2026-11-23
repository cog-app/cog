package web_test

import (
	"net/http"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/docs"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

// TestDocs: the docs are readable signed out, in Cog's layout, without the
// pages only the website has; their links to those pages go to the
// website, and only administrators see the administrators' section.
func TestDocs(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "root", "pass") // the first user is an administrator
	alice := env.CreateUser(t, "alice", "pass")
	require.True(t, admin.IsAdmin && !alice.IsAdmin)

	env.Get(t, "/docs/").Status(http.StatusOK).Contains("<title>Cog — Cog</title>")
	env.Get(t, "/docs/admin/https.html").Status(http.StatusOK).Contains(`class="sidebar-item active"`)
	env.Get(t, "/docs/getting-started/installation.html").Status(http.StatusNotFound)
	env.Get(t, "/docs/admin/cli.html").Status(http.StatusOK).
		Contains(`href="` + docs.SiteURL + `getting-started/installation.html"`)
	env.Get(t, "/docs/admin/").Status(http.StatusNotFound)

	env.Get(t, "/docs/", alice.Auth).Status(http.StatusOK).
		Contains(`<summary class="sidebar-heading">Using Cog</summary>`).
		NotContains(`<summary class="sidebar-heading">Administration</summary>`)
	env.Get(t, "/docs/", admin.Auth).Status(http.StatusOK).
		Contains(`<summary class="sidebar-heading">Administration</summary>`)
}

// TestSearchPartialHelp: the search box finds help pages, after the user's
// own results except on a help page.
func TestSearchPartialHelp(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	order := func(cur string) (help, pages int) {
		body := env.Get(t, "/api/search/partial?q=settings", alice.Auth,
			cogtest.Header("HX-Current-URL", env.Providers.Config.BaseURL+cur)).Status(http.StatusOK).ReadBody()
		require.Contains(t, body, `href="/docs/plugins/settings.html"`)
		return strings.Index(body, `<div class="search-section">Help</div>`), strings.Index(body, `<div class="search-section">Pages</div>`)
	}
	help, pages := order("/apps/files/")
	require.True(t, pages >= 0 && help > pages, "help after the app's pages")
	help, pages = order("/docs/using-cog/files.html")
	require.True(t, help >= 0 && help < pages, "help first on a help page")
}

// TestBrowserDocs: a docs page in Cog's layout with the user's navbar, its
// sidebar folded to the current section, and the search finding help.
func TestBrowserDocs(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/docs/using-cog/sharing.html")
	b.WaitVisible(t, ".docs-page h1")
	b.Snapshot(t)
	b.Type(t, "#search-box", "webdav")
	b.WaitVisible(t, "#search-results a[href='/docs/using-cog/webdav.html']")
	b.Snapshot(t, webtest.State("search"))
	b.Snapshot(t, webtest.Mobile)
}
