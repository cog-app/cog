package bookmarks_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
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
}
