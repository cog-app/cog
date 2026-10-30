package recipes_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
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
	env.CreateUser(t, "full", "pass")
	fullID := seedRecipes(t, env, "full")

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

	t.Run("FullRecipe", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/recipes/recipe/%d", fullID))
		b.WaitText(t, "Ingredients")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("FullEditForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/recipes/recipe/%d/edit", fullID))
		b.WaitVisible(t, "#ingredients")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ManyRecipes", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/recipes/")
		b.WaitText(t, "Overnight oats")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("KeywordView", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "full", "pass")
		b.Navigate(t, "/apps/recipes/keyword/vegetarian")
		b.WaitText(t, "Overnight oats")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// seedRecipes gives a user several recipes, one of them complete (times,
// yield, cuisine, calories, source, many ingredients, long steps, tags),
// with the clock advanced between inserts so the list order is fixed. It
// returns the complete recipe's id.
func seedRecipes(t *testing.T, env *cogtest.Env, username string) int64 {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(recipes.PluginID)
	require.True(t, ok, "recipes plugin not registered")
	db, err := raw.(*recipes.Plugin).DB(username)
	require.NoError(t, err)

	add := func(r recipes.Recipe, ingredients, steps, keywords []string) int64 {
		t.Helper()
		env.Clock.Advance(time.Minute)
		id, err := recipes.CreateRecipe(db, &r)
		require.NoError(t, err)
		require.NoError(t, recipes.SetIngredients(db, id, ingredients))
		require.NoError(t, recipes.SetInstructions(db, id, steps))
		require.NoError(t, recipes.SetKeywords(db, id, keywords))
		return id
	}

	full := add(recipes.Recipe{
		Name:        "Slow-roasted tomato and white bean soup with crispy garlic croutons",
		Description: "A thick, smoky soup that tastes like it took all day. Most of the time is hands-off in the oven, and the croutons can be made while the tomatoes roast.",
		SourceURL:   "https://www.recipes.example.com/soups/slow-roasted-tomato-and-white-bean-soup-with-crispy-garlic-croutons-1234567",
		Category:    "Soups",
		Cuisine:     "Italian",
		RecipeYield: "6 servings",
		PrepTime:    "PT20M",
		CookTime:    "PT1H30M",
		TotalTime:   "PT1H50M",
		Calories:    "410 kcal",
	}, []string{
		"1.5 kg ripe plum tomatoes, halved",
		"1 head garlic, cloves separated and peeled",
		"3 tbsp olive oil, plus more for drizzling",
		"2 × 400 g tins cannellini beans, drained and rinsed",
		"750 ml vegetable stock",
		"1 tsp smoked paprika",
		"Salt and freshly ground black pepper",
		"4 thick slices day-old sourdough, torn into bite-sized pieces",
		"A small handful of basil leaves, to serve",
	}, []string{
		"Heat the oven to 160°C (140°C fan). Spread the tomatoes cut-side up on a large roasting tray with half the garlic, drizzle with 2 tbsp oil, season generously, and roast for 1 hour 15 minutes until collapsed and caramelised at the edges.",
		"Scrape everything into a large pan, add the beans, stock and paprika, and simmer for 15 minutes.",
		"Meanwhile, toss the bread with the remaining oil and garlic (finely sliced) and bake for 10–12 minutes until golden.",
		"Blend half the soup until smooth, stir it back in, and adjust the seasoning.",
		"Serve topped with croutons, basil and a final drizzle of oil.",
	}, []string{"soup", "vegetarian", "make-ahead", "freezer-friendly", "winter"})

	add(recipes.Recipe{Name: "Overnight oats", Category: "Breakfast", TotalTime: "PT5M", RecipeYield: "1 serving"},
		[]string{"50 g oats", "150 ml milk"}, []string{"Mix and refrigerate overnight."}, []string{"vegetarian", "quick"})
	add(recipes.Recipe{Name: "Untitled experiment"}, nil, nil, nil)
	return full
}
