package bookmarks_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "edit", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "folder", "pass")
	env.CreateUser(t, "tags", "pass")
	env.CreateUser(t, "many", "pass")
	longID := seedManyBookmarks(t, env, "many")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "No bookmarks yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Create", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Example Site")
		b.Type(t, "#tags", "test,browser")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Example Site")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Edit", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Original BM")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Original BM")
		b.ClickText(t, "Edit")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Edited BM")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Edited BM")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Delete", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Delete Me")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Delete Me")
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `[hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "folder", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, `input[name="title"]`)
		// The folder form is the second form on the page (after the bookmark form).
		b.Eval(t, `() => {
			const forms = document.querySelectorAll('form[action="/apps/bookmarks/folders"]');
			if (forms.length > 0) {
				forms[0].querySelector('input[name="title"]').value = "My Folder";
				forms[0].submit();
			}
		}`)
		b.WaitText(t, "My Folder")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("TagsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "tags", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Type(t, "#url", "https://example.com")
		b.Type(t, "#title", "Tagged BM")
		b.Type(t, "#tags", "testtag")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Tagged BM")
		b.Navigate(t, "/apps/bookmarks/tags")
		b.WaitText(t, "testtag")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FolderView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "folder", "pass")
		// The CreateFolder test already created "My Folder" — navigate to it.
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "My Folder")
		b.Click(t, `a[href*="/apps/bookmarks/folder/"]`)
		b.WaitVisible(t, ".sidebar-content")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("TagView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "tags", "pass")
		// The TagsPage test already created "testtag" — navigate to it.
		b.Navigate(t, "/apps/bookmarks/tag/testtag")
		b.WaitText(t, "testtag")
		b.WaitText(t, "Tagged BM")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ExportPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "tags", "pass")
		// The export is a direct download — just verify the bookmarks index has content.
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "Tagged BM")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
	t.Run("ManyBookmarks", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "many", "pass")
		b.Navigate(t, "/apps/bookmarks/")
		b.WaitText(t, "Short")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("LongDetail", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "many", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/bookmarks/bookmark/%d", longID))
		b.WaitText(t, "Description:")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "many", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/bookmarks/bookmark/%d/edit", longID))
		b.WaitVisible(t, "#folder_id")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("NewForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "many", "pass")
		b.Navigate(t, "/apps/bookmarks/new")
		b.WaitVisible(t, "#url")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// seedManyBookmarks gives a user folders, tags and bookmarks with titles,
// URLs and tag lists long enough to stress the list and detail layouts. The
// clock advances between inserts so the list order is fixed. It returns the
// bookmark with the longest fields.
func seedManyBookmarks(t *testing.T, env *cogtest.Env, username string) int64 {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(bookmarks.PluginID)
	require.True(t, ok, "bookmarks plugin not registered")
	db, err := raw.(*bookmarks.Plugin).DB(username)
	require.NoError(t, err)

	reading, err := bookmarks.CreateFolder(db, "Reading List", nil)
	require.NoError(t, err)
	_, err = bookmarks.CreateFolder(db, "Work Reference Material For The Platform Team", nil)
	require.NoError(t, err)

	type bm struct {
		url, title, desc string
		tags             []string
		folders          []int64
	}
	var longID int64
	for i, c := range []bm{
		{"https://example.com/", "Short", "", nil, nil},
		{"https://docs.example.org/reference/api/v2/endpoints/authentication/oauth2/device-authorization-grant?lang=en&version=latest",
			"An extremely long bookmark title that describes the page in far more words than any reasonable title ever should",
			"A description long enough to wrap across several lines in the detail view, so that the spacing between the label and the paragraph, and between this block and the next, is visible in the snapshot.",
			[]string{"reference", "api", "authentication", "oauth", "documentation", "to-read", "work"}, []int64{reading.ID}},
		{"https://news.example.net/2026/03/14/story", "News story", "", []string{"news"}, []int64{reading.ID}},
		{"http://localhost:8080/dashboard", "Local dev dashboard", "", []string{"dev"}, nil},
	} {
		env.Clock.Advance(time.Minute)
		created, err := bookmarks.CreateBookmark(db, c.url, c.title, c.desc, c.tags, c.folders)
		require.NoError(t, err, "bookmark %d", i)
		if len(c.tags) > 5 {
			longID = created.ID
		}
	}
	return longID
}
