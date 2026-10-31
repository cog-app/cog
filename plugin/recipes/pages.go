package recipes

import (
	"database/sql"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/ui"
	"net/http"
	"net/url"
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
	// OOB renders the sidebar as an out-of-band swap, so deleting a row
	// brings its counts up to date along with the list.
	OOB bool
}

// The lists a recipe can be shown in.
const (
	viewAll      = "all"
	viewCategory = "category"
	viewKeyword  = "keyword"
)

// listView is one list of recipes and what it needs to render.
type listView struct {
	Kind    string
	Recipes []Recipe
}

func (h *Pages) buildSidebar(db *sql.DB, activePath string) (sidebarData, error) {
	count, err := RecipeCount(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("count recipes: %w", err)
	}
	cats, err := ListCategories(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("list categories: %w", err)
	}
	kws, err := ListKeywords(db)
	if err != nil {
		return sidebarData{}, fmt.Errorf("list tags: %w", err)
	}
	return sidebarData{
		RecipeCount: count,
		Categories:  cats,
		Keywords:    kws,
		ActivePath:  activePath,
	}, nil
}

// pageSetup opens the user's database and builds the sidebar for a page. It
// writes the error response itself and returns ok=false when it fails.
func (h *Pages) pageSetup(w http.ResponseWriter, r *http.Request, activePath string) (*sql.DB, sidebarData, bool) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	sb, err := h.buildSidebar(db, activePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil, sidebarData{}, false
	}
	return db, sb, true
}

// Index shows all recipes.
func (h *Pages) Index(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "/apps/recipes/")
	if !ok {
		return
	}
	recipes, err := ListRecipes(db, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Recipes")
	ui.Render(w, r, recipesIndexPage(sb, listView{Kind: viewAll, Recipes: recipes}))
}

// CategoryView shows recipes filtered by category.
func (h *Pages) CategoryView(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	db, sb, ok := h.pageSetup(w, r, "/apps/recipes/category/"+name)
	if !ok {
		return
	}
	recipes, err := RecipesByCategory(db, name, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, name)
	ui.Render(w, r, recipesCategoryPage(sb, name, listView{Kind: viewCategory, Recipes: recipes}))
}

// KeywordView shows recipes filtered by keyword tag.
func (h *Pages) KeywordView(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	db, sb, ok := h.pageSetup(w, r, "/apps/recipes/keyword/"+name)
	if !ok {
		return
	}
	recipes, err := RecipesByKeyword(db, name, 200)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	plugin.SetTitle(w, "Tagged "+name)
	ui.Render(w, r, recipesKeywordPage(sb, name, listView{Kind: viewKeyword, Recipes: recipes}))
}

// recipeDetail is a recipe with its lists.
type recipeDetail struct {
	Recipe       *Recipe
	Ingredients  []Ingredient
	Instructions []Instruction
	Keywords     []string
}

// loadRecipe reads the {id} path value and the recipe it names, with its
// lists. It writes the error response itself and returns nil when it fails.
func loadRecipe(w http.ResponseWriter, r *http.Request, db *sql.DB) *recipeDetail {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid recipe id", http.StatusBadRequest)
		return nil
	}
	recipe, err := GetRecipe(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return nil
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	d := &recipeDetail{Recipe: recipe}
	if d.Ingredients, err = GetIngredients(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if d.Instructions, err = GetInstructions(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	if d.Keywords, err = GetKeywords(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return nil
	}
	return d
}

// RecipeView shows a single recipe.
func (h *Pages) RecipeView(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "")
	if !ok {
		return
	}
	d := loadRecipe(w, r, db)
	if d == nil {
		return
	}
	plugin.SetTitle(w, d.Recipe.Name)
	ui.Render(w, r, recipeViewPage(sb, d))
}

// RecipeEdit shows the edit form for a recipe.
func (h *Pages) RecipeEdit(w http.ResponseWriter, r *http.Request) {
	db, sb, ok := h.pageSetup(w, r, "")
	if !ok {
		return
	}
	d := loadRecipe(w, r, db)
	if d == nil {
		return
	}
	plugin.SetTitle(w, "Edit "+d.Recipe.Name)
	ui.Render(w, r, recipeEditPage(sb, d, plugin.CSRFToken(r)))
}

// NewRecipe shows the create form.
func (h *Pages) NewRecipe(w http.ResponseWriter, r *http.Request) {
	_, sb, ok := h.pageSetup(w, r, "")
	if !ok {
		return
	}
	plugin.SetTitle(w, "New recipe")
	ui.Render(w, r, recipeNewPage(sb, plugin.CSRFToken(r)))
}

// ImportPage shows the import from URL form.
func (h *Pages) ImportPage(w http.ResponseWriter, r *http.Request) {
	_, sb, ok := h.pageSetup(w, r, "")
	if !ok {
		return
	}
	plugin.SetTitle(w, "Import recipe")
	ui.Render(w, r, recipeImportPage(sb, plugin.CSRFToken(r)))
}

// recipeFromForm copies the form's fields onto recipe.
func recipeFromForm(r *http.Request, recipe *Recipe) error {
	recipe.Name = strings.TrimSpace(r.FormValue("name"))
	recipe.Description = r.FormValue("description")
	recipe.Category = strings.TrimSpace(r.FormValue("category"))
	recipe.Cuisine = strings.TrimSpace(r.FormValue("cuisine"))
	recipe.RecipeYield = strings.TrimSpace(r.FormValue("recipe_yield"))
	recipe.Calories = strings.TrimSpace(r.FormValue("calories"))
	var err error
	if recipe.PrepTime, err = formDuration(r, "prep"); err != nil {
		return err
	}
	if recipe.CookTime, err = formDuration(r, "cook"); err != nil {
		return err
	}
	if recipe.TotalTime, err = formDuration(r, "total"); err != nil {
		return err
	}
	if recipe.Name == "" {
		return errors.New("recipe name is required")
	}
	return nil
}

// saveLists stores the recipe's ingredients, steps and tags from the form.
func saveLists(db *sql.DB, id int64, r *http.Request) error {
	if err := SetIngredients(db, id, splitLines(r.FormValue("ingredients"))); err != nil {
		return fmt.Errorf("save ingredients: %w", err)
	}
	if err := SetInstructions(db, id, splitLines(r.FormValue("instructions"))); err != nil {
		return fmt.Errorf("save instructions: %w", err)
	}
	if err := SetKeywords(db, id, splitComma(r.FormValue("keywords"))); err != nil {
		return fmt.Errorf("save tags: %w", err)
	}
	return nil
}

// CreateRecipe handles POST to create a new recipe.
func (h *Pages) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := plugin.UserDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recipe := &Recipe{}
	if err := recipeFromForm(r, recipe); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := CreateRecipe(db, recipe)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := saveLists(db, id, r); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := recipeFromForm(r, recipe); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := UpdateRecipe(db, recipe); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := saveLists(db, id, r); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps/recipes/recipe/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// DeleteRecipe handles DELETE to remove a recipe.
//
// From a list row (HX-Target names the row) the response removes the row and
// brings the sidebar, and the list's empty state, up to date out of band.
// From the recipe's own page, which no longer exists, the browser is sent to
// the list with a message.
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
	recipe, err := GetRecipe(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "recipe not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := DeleteRecipe(db, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if recipe.ImagePath != "" {
		if err := h.plugin.removeImage(plugin.Username(r), recipe.ImagePath); err != nil {
			h.plugin.log.Error("remove recipe image", "recipe", id, "error", err)
		}
	}

	if r.Header.Get("HX-Target") != recipeRowID(id) {
		plugin.SetFlash(w, plugin.Flash{Kind: "success", Message: fmt.Sprintf("Deleted “%s”.", recipe.Name)})
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("HX-Redirect", "/apps/recipes/")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/apps/recipes/", http.StatusSeeOther)
		return
	}

	page := ""
	if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
		page = u.Path
	}
	sb, err := h.buildSidebar(db, page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sb.OOB = true

	var empty *listView
	switch {
	case page == "/apps/recipes/" || page == "/apps/recipes":
		if sb.RecipeCount == 0 {
			empty = &listView{Kind: viewAll}
		}
	case strings.HasPrefix(page, "/apps/recipes/category/"):
		name := strings.TrimPrefix(page, "/apps/recipes/category/")
		if countOf(sb.Categories, name) == 0 {
			empty = &listView{Kind: viewCategory}
		}
	case strings.HasPrefix(page, "/apps/recipes/keyword/"):
		name := strings.TrimPrefix(page, "/apps/recipes/keyword/")
		if keywordCountOf(sb.Keywords, name) == 0 {
			empty = &listView{Kind: viewKeyword}
		}
	}
	ui.Render(w, r, rowDeleted(sb, empty))
}

func countOf(cats []CategoryCount, name string) int {
	for _, c := range cats {
		if c.Name == name {
			return c.Count
		}
	}
	return 0
}

func keywordCountOf(kws []KeywordCount, name string) int {
	for _, k := range kws {
		if k.Keyword == name {
			return k.Count
		}
	}
	return 0
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
		importFailed(w, r, "Enter the address of a recipe page to import.")
		return
	}

	result, err := h.plugin.FetchAndImport(db, plugin.Username(r), rawURL, r.UserAgent())
	if err != nil {
		h.plugin.log.Warn("recipe import failed", "url", rawURL, "err", err)
		importFailed(w, r, "Could not import that page: "+err.Error())
		return
	}

	http.Redirect(w, r, "/apps/recipes/recipe/"+strconv.FormatInt(result.RecipeID, 10), http.StatusSeeOther)
}

// importFailed sends the reason back to the import form. Answering the POST
// with an error page instead would render the message without the surrounding
// page, and leave the browser offering to re-submit the form on reload.
func importFailed(w http.ResponseWriter, r *http.Request, message string) {
	plugin.SetFlash(w, plugin.Flash{Kind: "error", Message: message, Form: map[string]string{"url": r.FormValue("url")}})
	http.Redirect(w, r, "/apps/recipes/import", http.StatusSeeOther)
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

func recipeRowID(id int64) string {
	return fmt.Sprintf("recipe-%d", id)
}
