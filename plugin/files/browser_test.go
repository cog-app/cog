package files_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, files.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "page", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "rename", "pass")
	env.CreateUser(t, "favorites", "pass")

	t.Run("PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/files/")
		b.WaitVisible(t, "#files-table")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Test Folder")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitVisible(t, "#files-table")
		b.WaitText(t, "Test Folder")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Doomed Folder")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitText(t, "Doomed Folder")
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `[hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("RenameFolder", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "rename", "pass")
		b.Navigate(t, "/apps/files/")
		b.Type(t, `[data-testid="new-folder-name"]`, "Old Name")
		b.Click(t, `[data-testid="new-folder-submit"]`)
		b.WaitText(t, "Old Name")
		// Override prompt to return the new name.
		b.Eval(t, `() => { window.prompt = () => "New Name" }`)
		b.Click(t, `[onclick*="renameFile"]`)
		b.WaitText(t, "New Name")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FavoritesPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "favorites", "pass")
		b.Navigate(t, "/apps/files/favorites")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
