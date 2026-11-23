package web_test

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	cogdocs "git.duckfam.us/jj-software/cog/DOCS"

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

// TestSearchPartialHelpAdmin: like the sidebar, the search leaves the
// administrators' help out for everyone else.
func TestSearchPartialHelpAdmin(t *testing.T) {
	env := cogtest.New(t)
	admin := env.CreateUser(t, "root", "pass") // the first user is an administrator
	alice := env.CreateUser(t, "alice", "pass")
	find := func(u *cogtest.User) string {
		return env.Get(t, "/api/search/partial?q=volumes", u.Auth,
			cogtest.Header("HX-Current-URL", env.Providers.Config.BaseURL+"/docs/")).Status(http.StatusOK).ReadBody()
	}
	require.Contains(t, find(admin), `href="/docs/admin/volumes.html"`)
	body := find(alice)
	require.NotContains(t, body, `href="/docs/admin/`)
	require.Contains(t, body, `href="/docs/using-cog/storage.html"`)
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

// TestDocsPart: a page's part under one heading, on its own, for a Help
// popover.
func TestDocsPart(t *testing.T) {
	env := cogtest.New(t)
	body := env.Get(t, "/docs/using-cog/sharing.html?part=permissions").Status(http.StatusOK).ReadBody()
	require.True(t, strings.HasPrefix(body, `<h2 id="permissions">Permissions</h2>`), body)
	require.NotContains(t, body, "<html")
	require.NotContains(t, body, `id="public-links"`, "ends at the next heading")
	env.Get(t, "/docs/using-cog/sharing.html?part=").Status(http.StatusOK).Contains(`<h1 id="sharing">Sharing</h1>`)
	env.Get(t, "/docs/using-cog/sharing.html?part=missing").Status(http.StatusNotFound)
}

// helpRef finds the docs a .templ file's Help icons point at.
var helpRef = regexp.MustCompile(`@(?:ui\.)?(?:Help\(|Help(?:Card|PageHeader)\("[^"]*",\s*)"([^"]*)"`)

// TestHelpRefs: every Help icon in Cog's templates points at a heading in
// the docs Cog serves, and only administrators' pages (admin*.templ) point
// at the administrators' docs.
func TestHelpRefs(t *testing.T) {
	d, err := docs.Load(cogdocs.FS, docs.Data{})
	require.NoError(t, err)
	root := filepath.Join("..", "..")
	n := 0
	err = filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() && path != root && strings.HasPrefix(e.Name(), ".") || e.IsDir() && e.Name() == "tmp" {
			return filepath.SkipDir
		}
		if e.IsDir() || filepath.Ext(path) != ".templ" {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range helpRef.FindAllStringSubmatch(string(src), -1) {
			n++
			page, id, _ := strings.Cut(m[1], "#")
			p := d.Page(page)
			if p == nil {
				t.Errorf("%s: help for %s: no such page", path, m[1])
				continue
			}
			if _, ok := p.Part(id); !ok {
				t.Errorf("%s: help for %s: no such heading", path, m[1])
			}
			if strings.HasPrefix(page, docs.AdminSection+"/") && !strings.HasPrefix(filepath.Base(path), "admin") {
				t.Errorf("%s: help for %s: a page for everyone points at the administrators' docs", path, m[1])
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.True(t, n >= 20, "found the Help icons")
}

// TestBrowserHelp: hovering a Help icon shows its part of the docs, with
// links that lead to the docs.
func TestBrowserHelp(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.Login(t, "alice", "pass")
	b.Navigate(t, "/settings/security")
	b.Hover(t, `a[data-help][href="/docs/using-cog/app-passwords.html"]`)
	b.WaitVisible(t, "#help-popover h1")
	require.Equal(t, "App Passwords", b.Eval(t, `document.querySelector("#help-popover h1").textContent`))
	require.Equal(t, "/docs/using-cog/app-passwords.html",
		b.Eval(t, `new URL(document.querySelector("#help-popover .help-more").href).pathname`))
	b.Snapshot(t)

	// Into the popover and back onto the icon: it stays.
	b.Hover(t, "#help-popover .help-more")
	b.Hover(t, `a[data-help][href="/docs/using-cog/app-passwords.html"]`)
	time.Sleep(500 * time.Millisecond) // past the popover's 200ms hide delay
	require.Equal(t, false, b.Eval(t, `document.querySelector("#help-popover").hidden`))
}
