package recipes

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Pages holds the web UI handlers for the recipes plugin.
type Pages struct {
	plugin *Plugin
}

// NewPages creates a new Pages handler.
func NewPages(p *Plugin) *Pages {
	return &Pages{plugin: p}
}

type sidebarData struct {
	RecipeCount int
	Categories  []CategoryCount
	Keywords    []KeywordCount
	ActivePath  string
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) sidebarData {
	count, _ := RecipeCount(db)
	cats, _ := ListCategories(db)
	kws, _ := ListKeywords(db)
	return sidebarData{
		RecipeCount: count,
		Categories:  cats,
		Keywords:    kws,
		ActivePath:  activePath,
	}
}

// Index shows all recipes.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/recipes/")
	recipes, _ := ListRecipes(db, 200)
	plugin.SetTitle(w, "Recipes")
	recipesIndexPage(sb, recipes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CategoryView shows recipes filtered by category.
func (h *Pages) CategoryView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	sb := h.buildSidebar(db, "/apps/recipes/category/"+name)
	recipes, _ := RecipesByCategory(db, name, 200)
	plugin.SetTitle(w, name)
	recipesCategoryPage(sb, name, recipes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// KeywordView shows recipes filtered by keyword tag.
func (h *Pages) KeywordView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	name := r.PathValue("name")
	sb := h.buildSidebar(db, "/apps/recipes/keyword/"+name)
	recipes, _ := RecipesByKeyword(db, name, 200)
	plugin.SetTitle(w, name)
	recipesKeywordPage(sb, name, recipes, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// RecipeView shows a single recipe.
func (h *Pages) RecipeView(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid recipe id", http.StatusBadRequest)
		return
	}
	recipe, err := GetRecipe(db, id)
	if err != nil {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return
	}
	ingredients, _ := GetIngredients(db, id)
	instructions, _ := GetInstructions(db, id)
	keywords, _ := GetKeywords(db, id)

	sb := h.buildSidebar(db, "/apps/recipes/recipe/"+r.PathValue("id"))
	plugin.SetTitle(w, recipe.Name)
	recipeViewPage(sb, recipe, ingredients, instructions, keywords, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// RecipeEdit shows the edit form for a recipe.
func (h *Pages) RecipeEdit(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid recipe id", http.StatusBadRequest)
		return
	}
	recipe, err := GetRecipe(db, id)
	if err != nil {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return
	}
	ingredients, _ := GetIngredients(db, id)
	instructions, _ := GetInstructions(db, id)
	keywords, _ := GetKeywords(db, id)
	cats, _ := ListCategories(db)

	sb := h.buildSidebar(db, "/apps/recipes/recipe/"+r.PathValue("id")+"/edit")
	plugin.SetTitle(w, "Edit: "+recipe.Name)
	recipeEditPage(sb, recipe, ingredients, instructions, keywords, cats, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// NewRecipe shows the create form.
func (h *Pages) NewRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cats, _ := ListCategories(db)
	sb := h.buildSidebar(db, "/apps/recipes/new")
	plugin.SetTitle(w, "New Recipe")
	recipeNewPage(sb, cats, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// ImportPage shows the import from URL form.
func (h *Pages) ImportPage(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb := h.buildSidebar(db, "/apps/recipes/import")
	plugin.SetTitle(w, "Import Recipe")
	recipeImportPage(sb, plugin.CSRFToken(r)).Render(r.Context(), w)
}

// CreateRecipe handles POST to create a new recipe.
func (h *Pages) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	recipe := &Recipe{
		Name:        r.FormValue("name"),
		Description: r.FormValue("description"),
		Category:    r.FormValue("category"),
		Cuisine:     r.FormValue("cuisine"),
		RecipeYield: r.FormValue("recipe_yield"),
		PrepTime:    r.FormValue("prep_time"),
		CookTime:    r.FormValue("cook_time"),
		TotalTime:   r.FormValue("total_time"),
	}
	if recipe.Name == "" {
		http.Error(w, "recipe name is required", http.StatusBadRequest)
		return
	}

	id, err := CreateRecipe(db, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ingredients := splitLines(r.FormValue("ingredients"))
	SetIngredients(db, id, ingredients)

	instructions := splitLines(r.FormValue("instructions"))
	SetInstructions(db, id, instructions)

	keywords := splitComma(r.FormValue("keywords"))
	SetKeywords(db, id, keywords)

	http.Redirect(w, r, "/apps/recipes/recipe/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// UpdateRecipe handles POST to update an existing recipe.
func (h *Pages) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid recipe id", http.StatusBadRequest)
		return
	}
	recipe, err := GetRecipe(db, id)
	if err != nil {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return
	}

	recipe.Name = r.FormValue("name")
	recipe.Description = r.FormValue("description")
	recipe.Category = r.FormValue("category")
	recipe.Cuisine = r.FormValue("cuisine")
	recipe.RecipeYield = r.FormValue("recipe_yield")
	recipe.PrepTime = r.FormValue("prep_time")
	recipe.CookTime = r.FormValue("cook_time")
	recipe.TotalTime = r.FormValue("total_time")

	if recipe.Name == "" {
		http.Error(w, "recipe name is required", http.StatusBadRequest)
		return
	}

	if err := UpdateRecipe(db, recipe); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ingredients := splitLines(r.FormValue("ingredients"))
	SetIngredients(db, id, ingredients)

	instructions := splitLines(r.FormValue("instructions"))
	SetInstructions(db, id, instructions)

	keywords := splitComma(r.FormValue("keywords"))
	SetKeywords(db, id, keywords)

	http.Redirect(w, r, "/apps/recipes/recipe/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// DeleteRecipe handles DELETE to remove a recipe.
func (h *Pages) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	DeleteRecipe(db, id)
	w.WriteHeader(http.StatusOK)
}

// ImportFromURL handles POST to import a recipe from a URL.
func (h *Pages) ImportFromURL(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rawURL := r.FormValue("url")
	if rawURL == "" {
		http.Error(w, "URL is required", http.StatusBadRequest)
		return
	}

	result, err := h.plugin.FetchAndImport(db, plugin.Username(r), rawURL, r.UserAgent())
	if err != nil {
		http.Error(w, "Import failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	http.Redirect(w, r, "/apps/recipes/recipe/"+strconv.FormatInt(result.RecipeID, 10), http.StatusSeeOther)
}

func splitLines(s string) []string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}

func splitComma(s string) []string {
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
