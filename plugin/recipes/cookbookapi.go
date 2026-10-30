package recipes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// CookbookAPI handles NextCloud Cookbook API compatibility endpoints.
type CookbookAPI struct {
	plugin *Plugin
}

// NewCookbookAPI creates a new CookbookAPI handler.
func NewCookbookAPI(p *Plugin) *CookbookAPI {
	return &CookbookAPI{plugin: p}
}

func (a *CookbookAPI) userDB(r *http.Request) (*sql.DB, error) {
	u := plugin.UserFromContext(r.Context())
	return a.plugin.DB(u.Username)
}

func (a *CookbookAPI) username(r *http.Request) string {
	return plugin.UserFromContext(r.Context()).Username
}

type recipeListItem struct {
	RecipeID     int64  `json:"recipe_id"`
	Name         string `json:"name"`
	Keywords     string `json:"keywords"`
	DateCreated  string `json:"dateCreated"`
	DateModified string `json:"dateModified"`
}

// ListRecipes returns all recipes as a JSON array.
func (a *CookbookAPI) ListRecipes(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	recipes, err := ListRecipes(db, 1000)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var items []recipeListItem
	for _, recipe := range recipes {
		kws, _ := GetKeywords(db, recipe.ID)
		var kwStr strings.Builder
		for i, kw := range kws {
			if i > 0 {
				kwStr.WriteString(",")
			}
			kwStr.WriteString(kw)
		}
		items = append(items, recipeListItem{
			RecipeID:     recipe.ID,
			Name:         recipe.Name,
			Keywords:     kwStr.String(),
			DateCreated:  formatUnixISO(recipe.CreatedAt),
			DateModified: formatUnixISO(recipe.UpdatedAt),
		})
	}
	if items == nil {
		items = []recipeListItem{}
	}
	plugin.WriteJSON(w, http.StatusOK, items)
}

// GetRecipe returns the full recipe JSON-LD.
func (a *CookbookAPI) GetRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
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
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// If we have stored JSON-LD, return it directly
	if recipe.JSONLD != "" && recipe.JSONLD != "{}" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(recipe.JSONLD))
		return
	}

	// Otherwise, build JSON-LD from stored data
	ingredients, _ := GetIngredients(db, id)
	instructions, _ := GetInstructions(db, id)
	keywords, _ := GetKeywords(db, id)

	ld := buildJSONLD(recipe, ingredients, instructions, keywords)
	plugin.WriteJSON(w, http.StatusOK, ld)
}

// GetRecipeImage serves the recipe image.
func (a *CookbookAPI) GetRecipeImage(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
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
	if errors.Is(err, sql.ErrNoRows) || (err == nil && recipe.ImagePath == "") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.ServeFile(w, r, a.plugin.imagePath(a.username(r), recipe.ImagePath))
}

// ListCategories returns all categories with counts.
func (a *CookbookAPI) ListCategories(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	cats, err := ListCategories(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cats == nil {
		cats = []CategoryCount{}
	}
	plugin.WriteJSON(w, http.StatusOK, cats)
}

// ListKeywords returns all keywords with counts.
func (a *CookbookAPI) ListKeywords(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	kws, err := ListKeywords(db)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if kws == nil {
		kws = []KeywordCount{}
	}
	plugin.WriteJSON(w, http.StatusOK, kws)
}

// CreateRecipe creates a recipe from a JSON-LD body.
func (a *CookbookAPI) CreateRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	parsed, err := ImportRecipeFromJSON(string(body))
	if err != nil {
		http.Error(w, "invalid recipe JSON-LD: "+err.Error(), http.StatusBadRequest)
		return
	}

	id, err := SaveImportedRecipe(db, parsed)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]int64{"id": id})
}

// UpdateRecipe updates a recipe from a JSON-LD body.
func (a *CookbookAPI) UpdateRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}

	existing, err := GetRecipe(db, id)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodySize))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}

	parsed, err := ImportRecipeFromJSON(string(body))
	if err != nil {
		http.Error(w, "invalid recipe JSON-LD: "+err.Error(), http.StatusBadRequest)
		return
	}

	existing.Name = parsed.Name
	existing.Description = parsed.Description
	existing.Category = parsed.Category
	existing.Cuisine = parsed.Cuisine
	existing.RecipeYield = parsed.RecipeYield
	existing.PrepTime = parsed.PrepTime
	existing.CookTime = parsed.CookTime
	existing.TotalTime = parsed.TotalTime
	existing.Calories = parsed.Calories
	existing.JSONLD = parsed.JSONLD

	if err := UpdateRecipe(db, existing); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	SetIngredients(db, id, parsed.Ingredients)
	SetInstructions(db, id, parsed.Instructions)
	SetKeywords(db, id, parsed.Keywords)

	plugin.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DeleteRecipe deletes a recipe.
func (a *CookbookAPI) DeleteRecipe(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
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
	plugin.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ImportFromURL imports a recipe from a URL (API endpoint).
func (a *CookbookAPI) ImportFromURL(w http.ResponseWriter, r *http.Request) {
	db, err := a.userDB(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.URL == "" {
		http.Error(w, "url is required", http.StatusBadRequest)
		return
	}

	result, err := a.plugin.FetchAndImport(db, a.username(r), req.URL, r.UserAgent())
	if err != nil {
		http.Error(w, "import failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]int64{"id": result.RecipeID})
}

func formatUnixISO(unix int64) string {
	if unix == 0 {
		return ""
	}
	return strconv.FormatInt(unix, 10)
}

// buildJSONLD constructs a schema.org Recipe JSON-LD object from stored data.
func buildJSONLD(recipe *Recipe, ingredients []Ingredient, instructions []Instruction, keywords []string) map[string]any {
	ld := map[string]any{
		"@context": "https://schema.org",
		"@type":    "Recipe",
		"name":     recipe.Name,
	}
	if recipe.Description != "" {
		ld["description"] = recipe.Description
	}
	if recipe.Category != "" {
		ld["recipeCategory"] = recipe.Category
	}
	if recipe.Cuisine != "" {
		ld["recipeCuisine"] = recipe.Cuisine
	}
	if recipe.RecipeYield != "" {
		ld["recipeYield"] = recipe.RecipeYield
	}
	if recipe.PrepTime != "" {
		ld["prepTime"] = recipe.PrepTime
	}
	if recipe.CookTime != "" {
		ld["cookTime"] = recipe.CookTime
	}
	if recipe.TotalTime != "" {
		ld["totalTime"] = recipe.TotalTime
	}
	if recipe.SourceURL != "" {
		ld["url"] = recipe.SourceURL
	}

	if len(ingredients) > 0 {
		var items []string
		for _, i := range ingredients {
			items = append(items, i.Text)
		}
		ld["recipeIngredient"] = items
	}

	if len(instructions) > 0 {
		var steps []map[string]string
		for _, i := range instructions {
			steps = append(steps, map[string]string{
				"@type": "HowToStep",
				"text":  i.Text,
			})
		}
		ld["recipeInstructions"] = steps
	}

	if len(keywords) > 0 {
		var kwStr strings.Builder
		for i, kw := range keywords {
			if i > 0 {
				kwStr.WriteString(", ")
			}
			kwStr.WriteString(kw)
		}
		ld["keywords"] = kwStr.String()
	}

	if recipe.Calories != "" {
		ld["nutrition"] = map[string]any{
			"@type":    "NutritionInformation",
			"calories": recipe.Calories,
		}
	}

	return ld
}
