package notes_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/notes"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, notes.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "edit", "pass")
	env.CreateUser(t, "star", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "starred", "pass")
	env.CreateUser(t, "category", "pass")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "No notes yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Create", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "My Test Note")
		b.Type(t, "#content", "Hello from browser test")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "My Test Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Edit", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Original Title")
		b.Type(t, "#content", "Original content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Original Title")
		b.Click(t, `.btn-outline`)
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Edited Title")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Edited Title")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Star", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "star", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Star Me")
		b.Type(t, "#content", "Star content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Star Me")
		// Star button is on the list page, not the detail page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Star Me")
		b.Click(t, `[title="Toggle star"]`)
		b.WaitVisible(t, `[title="Toggle star"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Delete", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Delete Me")
		b.Type(t, "#content", "Delete content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Delete Me")
		// Delete button is on the list page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Delete Me")
		b.AcceptConfirms(t)
		b.Click(t, `[hx-delete]`)
		b.WaitNotVisible(t, `[hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CategoryView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "category", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Work Note")
		b.Type(t, "#content", "Important work stuff")
		b.Type(t, "#category", "Work")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Work Note")
		// Navigate to the category page via sidebar.
		b.Navigate(t, "/apps/notes/category/Work")
		b.WaitText(t, "Work Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Starred", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "starred", "pass")
		b.Navigate(t, "/apps/notes/new")
		b.WaitVisible(t, "#title")
		b.Type(t, "#title", "Starred Note")
		b.Type(t, "#content", "Content")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Starred Note")
		// Star button is on the list page.
		b.Navigate(t, "/apps/notes/")
		b.WaitText(t, "Starred Note")
		b.Click(t, `[title="Toggle star"]`)
		b.WaitVisible(t, `[title="Toggle star"]`)
		b.Navigate(t, "/apps/notes/starred")
		b.WaitText(t, "Starred Note")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
