package docs

import (
	"bytes"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	cogdocs "git.duckfam.us/jj-software/cog/DOCS"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/ui"
)

func file(s string) *fstest.MapFile { return &fstest.MapFile{Data: []byte(s)} }

var testFS = fstest.MapFS{
	"index.md": file("# Cog\n\nStart with [installing](getting-started/installation.md#download) " +
		"or [the CLI](admin/cli.md).\n"),
	"getting-started/installation.md": file("{{ if not .Site }}{{ skip }}{{ end }}# Installation\n\n## Download\n\nGet it.\n"),
	"admin/cli.md": file("# Command line\n\n## Backups\n\nRun `cog backup` for Cog {{ .Version }}.\n\n" +
		"```\n{{`{{.Year}}`}}\n```\n\nSee [the overview](../index.md).\n"),
}

// TestLoad: a page's template decides what it says and whether it is there,
// and a link to a page left out of Cog goes to the website.
func TestLoad(t *testing.T) {
	app, err := Load(testFS, Data{Version: "1.2.3"})
	require.NoError(t, err)
	require.Equal(t, 2, len(app.Pages))
	require.Nil(t, app.Page("getting-started/installation.html"), "left out of Cog")
	idx := app.Page("")
	require.NotNil(t, idx)
	require.Contains(t, string(idx.Body), `href="`+SiteURL+`getting-started/installation.html#download"`)
	require.Contains(t, string(idx.Body), `href="admin/cli.html"`)
	cli := app.Page("admin/cli.html")
	require.Equal(t, "Command line", cli.Title)
	require.Equal(t, "Administration", cli.Section)
	require.Contains(t, string(cli.Body), "Cog 1.2.3")
	require.Contains(t, string(cli.Body), "{{.Year}}")
	require.Contains(t, string(cli.Body), `href="../index.html"`)

	site, err := Load(testFS, Data{Site: true})
	require.NoError(t, err)
	require.Equal(t, 3, len(site.Pages))
	require.Contains(t, string(site.Page("index.html").Body), `href="getting-started/installation.html#download"`)
	require.Equal(t, []string{"index.md", "getting-started/installation.md", "admin/cli.md"},
		[]string{site.Pages[0].Source, site.Pages[1].Source, site.Pages[2].Source}, "overview, then sections in order")
}

// TestLoadMistakes: a broken link, a page with no title and a bad template
// stop the build rather than publishing a broken page.
func TestLoadMistakes(t *testing.T) {
	for name, src := range map[string]string{
		"links to admin/missing.md":     "# Cog\n\n[gone](admin/missing.md)\n",
		"no # title":                    "Just text.\n",
		"function \"nope\" not defined": "# Cog\n\n{{ nope }}\n",
	} {
		_, err := Load(fstest.MapFS{"index.md": file(src)}, Data{})
		require.Error(t, err)
		require.Contains(t, err.Error(), name)
	}
}

// TestDocuments: a page in a search index is its title, section, headings
// and text, found by its path.
func TestDocuments(t *testing.T) {
	d, err := Load(testFS, Data{Site: true})
	require.NoError(t, err)
	docs := d.Documents()
	require.Equal(t, 3, len(docs))
	cli := docs[2]
	require.Equal(t, "docs:admin/cli.html", cli.ID)
	require.Equal(t, "admin/cli.html", cli.Fields["_id"])
	require.Equal(t, "Command line", cli.Fields["name"])
	require.Equal(t, "Administration", cli.Fields["subtext"])
	require.Contains(t, cli.Fields["content"].(string), "Backups\nCommand line Backups Run cog backup for Cog")
}

// TestSidebar: the current page's section is open and the others closed,
// and the administrators' section is only there for administrators.
func TestSidebar(t *testing.T) {
	d, err := Load(fstest.MapFS{
		"index.md":       file("# Cog\n"),
		"admin/cli.md":   file("# Command line\n"),
		"using-cog/x.md": file("# Files\n"),
	}, Data{})
	require.NoError(t, err)
	files := d.Page("using-cog/x.html")
	html, err := d.Sidebar("/docs/", files, true)
	require.NoError(t, err)
	flat := strings.Join(strings.Fields(string(html)), " ")
	require.Contains(t, flat, `<details class="sidebar-section" open> <summary class="sidebar-heading">Using Cog</summary> `+
		`<a href="/docs/using-cog/x.html" class="sidebar-item active" aria-current="page">Files</a>`)
	require.Contains(t, flat, `<details class="sidebar-section"> <summary class="sidebar-heading">Administration</summary>`)
	html, err = d.Sidebar("/docs/", files, false)
	require.NoError(t, err)
	require.NotContains(t, string(html), "Administration")
	html, err = d.Sidebar("/docs/", d.Page("admin/cli.html"), false)
	require.NoError(t, err)
	require.Contains(t, string(html), "Administration", "on one of its pages")
}

// TestCogDocs: Cog's own docs build, in Cog and for the website, and every
// page is in the search index.
func TestCogDocs(t *testing.T) {
	for _, data := range []Data{{}, {Site: true}} {
		d, err := Load(cogdocs.FS, data)
		require.NoError(t, err)
		require.Equal(t, data.Site, d.Page("getting-started/installation.html") != nil)
		var buf bytes.Buffer
		for _, p := range d.Pages {
			buf.Reset()
			require.NoError(t, d.Render(&buf, p, Assets{}))
			require.True(t, bytes.Contains(d.Index(), []byte(`"url":"`+p.Path+`"`)), p.Path)
		}
	}
}

// TestWrite: the website is the pages, the index, and the assets they load
// at paths relative to each page.
func TestWrite(t *testing.T) {
	d, err := Load(testFS, Data{Site: true})
	require.NoError(t, err)
	static := fstest.MapFS{}
	for _, name := range StaticFiles {
		static[name] = file("/* " + name + " */")
	}
	dir := filepath.Join(t.TempDir(), "site")
	require.NoError(t, d.Write(dir, static))
	var got []string
	require.NoError(t, fs.WalkDir(os.DirFS(dir), ".", func(name string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			got = append(got, name)
		}
		return err
	}))
	require.Equal(t, "admin/cli.html getting-started/installation.html index.html search.json "+
		"static/cog.js static/docs-search.js static/favicon.svg static/style.css", strings.Join(got, " "))
	cli, err := os.ReadFile(filepath.Join(dir, "admin/cli.html"))
	require.NoError(t, err)
	for _, want := range []string{
		`href="../static/style.css"`, `data-docs-index="../search.json"`,
		`<a href="../admin/cli.html" class="sidebar-item active" aria-current="page">Command line</a>`,
		`href="https://github.com/cog-app/cog/releases/latest"`,
	} {
		require.Contains(t, string(cli), want)
	}
}

// TestBrowserSite: the website as a visitor sees it, in Cog's look, and
// its search, which runs in the browser over search.json.
func TestBrowserSite(t *testing.T) {
	d, err := Load(cogdocs.FS, Data{Site: true})
	require.NoError(t, err)
	static, err := fs.Sub(ui.StaticFS, "static")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, d.Write(dir, static))

	eng := webtest.New(http.FileServer(http.Dir(dir)))
	t.Cleanup(eng.Close)
	b := eng.Start(t)
	b.Navigate(t, "/getting-started/installation.html")
	b.WaitVisible(t, ".docs-page h1")
	b.Snapshot(t)
	b.Type(t, "#search-box", "webdav")
	b.WaitVisible(t, "#search-results a[href='../using-cog/webdav.html']")
	b.Snapshot(t, webtest.State("search"))
}
