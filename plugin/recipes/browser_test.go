package recipes_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/recipes"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, recipes.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "create", "pass")
	env.CreateUser(t, "edit", "pass")
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "category", "pass")
	env.CreateUser(t, "import", "pass")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/recipes/")
		b.WaitText(t, "No recipes yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateAndView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/recipes/new")
		b.WaitVisible(t, "#name")
		b.Type(t, "#name", "Test Pasta")
		b.Type(t, "#description", "A simple pasta dish")
		b.Type(t, "#category", "Main Course")
		b.Type(t, "#ingredients", "200g pasta\n100g sauce")
		b.Type(t, "#instructions", "Boil pasta\nAdd sauce")
		b.Type(t, "#keywords", "italian, pasta")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Test Pasta")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EditRecipe", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "edit", "pass")
		b.Navigate(t, "/apps/recipes/new")
		b.WaitVisible(t, "#name")
		b.Type(t, "#name", "Original Recipe")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Original Recipe")
		b.ClickText(t, "Edit")
		b.WaitVisible(t, "#name")
		b.Type(t, "#name", "Edited Recipe")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Edited Recipe")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DeleteRecipe", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/recipes/new")
		b.WaitVisible(t, "#name")
		b.Type(t, "#name", "Delete Me")
		b.Click(t, `main button[type="submit"]`)
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
		b.Navigate(t, "/apps/recipes/new")
		b.WaitVisible(t, "#name")
		b.Type(t, "#name", "Category Soup")
		b.Type(t, "#category", "Soups")
		b.Click(t, `main button[type="submit"]`)
		b.WaitText(t, "Category Soup")
		b.Navigate(t, "/apps/recipes/category/Soups")
		b.WaitText(t, "Category Soup")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ImportPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "import", "pass")
		b.Navigate(t, "/apps/recipes/import")
		b.WaitText(t, "Import Recipe")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
