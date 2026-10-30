package recipes

import (
	"database/sql"
	"strings"
)

// Recipe is a recipe with metadata stored in SQLite.
type Recipe struct {
	ID          int64
	Name        string
	Description string
	SourceURL   string
	ImagePath   string
	Category    string
	Cuisine     string
	RecipeYield string
	PrepTime    string
	CookTime    string
	TotalTime   string
	Calories    string
	JSONLD      string
	ETag        string
	CreatedAt   int64
	UpdatedAt   int64
}

// Ingredient is a single ingredient line.
type Ingredient struct {
	ID        int64
	RecipeID  int64
	SortOrder int
	Text      string
}

// Instruction is a single instruction step.
type Instruction struct {
	ID        int64
	RecipeID  int64
	SortOrder int
	Text      string
}

// CategoryCount holds a category name and its recipe count.
type CategoryCount struct {
	Name  string
	Count int
}

// KeywordCount holds a keyword and its recipe count.
type KeywordCount struct {
	Keyword string
	Count   int
}

// --- Recipe CRUD ---

const recipeCols = `id, name, description, source_url, image_path, category, cuisine,
	recipe_yield, prep_time, cook_time, total_time, calories, json_ld, etag, created_at, updated_at`

func scanRecipe(s interface{ Scan(...any) error }) (*Recipe, error) {
	var r Recipe
	err := s.Scan(&r.ID, &r.Name, &r.Description, &r.SourceURL, &r.ImagePath,
		&r.Category, &r.Cuisine, &r.RecipeYield, &r.PrepTime, &r.CookTime,
		&r.TotalTime, &r.Calories, &r.JSONLD, &r.ETag, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func queryRecipes(db *sql.DB, query string, args ...any) ([]Recipe, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var recipes []Recipe
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		recipes = append(recipes, *r)
	}
	return recipes, rows.Err()
}

func ListRecipes(db *sql.DB, limit int) ([]Recipe, error) {
	return queryRecipes(db, `SELECT `+recipeCols+`
		FROM recipes ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
}

func RecipesByCategory(db *sql.DB, category string, limit int) ([]Recipe, error) {
	return queryRecipes(db, `SELECT `+recipeCols+`
		FROM recipes WHERE category = ? ORDER BY updated_at DESC, id DESC LIMIT ?`, category, limit)
}

func RecipesByKeyword(db *sql.DB, keyword string, limit int) ([]Recipe, error) {
	return queryRecipes(db, `SELECT `+recipeCols+`
		FROM recipes r
		JOIN keywords k ON k.recipe_id = r.id
		WHERE k.keyword = ?
		ORDER BY r.updated_at DESC, r.id DESC LIMIT ?`, keyword, limit)
}

func RecentRecipes(db *sql.DB, limit int) ([]Recipe, error) {
	return queryRecipes(db, `SELECT `+recipeCols+`
		FROM recipes ORDER BY updated_at DESC, id DESC LIMIT ?`, limit)
}

func GetRecipe(db *sql.DB, id int64) (*Recipe, error) {
	return scanRecipe(db.QueryRow(`SELECT `+recipeCols+`
		FROM recipes WHERE id = ?`, id))
}

func CreateRecipe(db *sql.DB, r *Recipe) (int64, error) {
	res, err := db.Exec(`INSERT INTO recipes (name, description, source_url, image_path,
		category, cuisine, recipe_yield, prep_time, cook_time, total_time, calories, json_ld, etag)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Name, r.Description, r.SourceURL, r.ImagePath,
		r.Category, r.Cuisine, r.RecipeYield, r.PrepTime, r.CookTime,
		r.TotalTime, r.Calories, r.JSONLD, r.ETag)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UpdateRecipe(db *sql.DB, r *Recipe) error {
	_, err := db.Exec(`UPDATE recipes SET name = ?, description = ?, source_url = ?,
		image_path = ?, category = ?, cuisine = ?, recipe_yield = ?,
		prep_time = ?, cook_time = ?, total_time = ?, calories = ?,
		json_ld = ?, etag = ?, updated_at = unixepoch()
		WHERE id = ?`,
		r.Name, r.Description, r.SourceURL, r.ImagePath,
		r.Category, r.Cuisine, r.RecipeYield, r.PrepTime, r.CookTime,
		r.TotalTime, r.Calories, r.JSONLD, r.ETag, r.ID)
	return err
}

func DeleteRecipe(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM recipes WHERE id = ?`, id)
	return err
}

func RecipeCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM recipes`).Scan(&n)
	return n, err
}

// --- Ingredients ---

func GetIngredients(db *sql.DB, recipeID int64) ([]Ingredient, error) {
	rows, err := db.Query(`SELECT id, recipe_id, sort_order, text
		FROM recipe_ingredients WHERE recipe_id = ? ORDER BY sort_order`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Ingredient
	for rows.Next() {
		var i Ingredient
		if err := rows.Scan(&i.ID, &i.RecipeID, &i.SortOrder, &i.Text); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func SetIngredients(db *sql.DB, recipeID int64, texts []string) error {
	if _, err := db.Exec(`DELETE FROM recipe_ingredients WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := db.Exec(`INSERT INTO recipe_ingredients (recipe_id, sort_order, text) VALUES (?, ?, ?)`,
			recipeID, i, t); err != nil {
			return err
		}
	}
	return nil
}

// IngredientsText returns all ingredients for a recipe as a single string (for search indexing).
func IngredientsText(db *sql.DB, recipeID int64) string {
	items, err := GetIngredients(db, recipeID)
	if err != nil {
		return ""
	}
	var parts []string
	for _, i := range items {
		parts = append(parts, i.Text)
	}
	return strings.Join(parts, "\n")
}

// --- Instructions ---

func GetInstructions(db *sql.DB, recipeID int64) ([]Instruction, error) {
	rows, err := db.Query(`SELECT id, recipe_id, sort_order, text
		FROM recipe_instructions WHERE recipe_id = ? ORDER BY sort_order`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Instruction
	for rows.Next() {
		var i Instruction
		if err := rows.Scan(&i.ID, &i.RecipeID, &i.SortOrder, &i.Text); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	return items, rows.Err()
}

func SetInstructions(db *sql.DB, recipeID int64, texts []string) error {
	if _, err := db.Exec(`DELETE FROM recipe_instructions WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for i, t := range texts {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := db.Exec(`INSERT INTO recipe_instructions (recipe_id, sort_order, text) VALUES (?, ?, ?)`,
			recipeID, i, t); err != nil {
			return err
		}
	}
	return nil
}

// InstructionsText returns all instructions as a single string (for search indexing).
func InstructionsText(db *sql.DB, recipeID int64) string {
	items, err := GetInstructions(db, recipeID)
	if err != nil {
		return ""
	}
	var parts []string
	for _, i := range items {
		parts = append(parts, i.Text)
	}
	return strings.Join(parts, "\n")
}

// --- Keywords ---

func GetKeywords(db *sql.DB, recipeID int64) ([]string, error) {
	rows, err := db.Query(`SELECT keyword FROM keywords WHERE recipe_id = ? ORDER BY keyword`, recipeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kws []string
	for rows.Next() {
		var kw string
		if err := rows.Scan(&kw); err != nil {
			return nil, err
		}
		kws = append(kws, kw)
	}
	return kws, rows.Err()
}

func SetKeywords(db *sql.DB, recipeID int64, keywords []string) error {
	if _, err := db.Exec(`DELETE FROM keywords WHERE recipe_id = ?`, recipeID); err != nil {
		return err
	}
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if _, err := db.Exec(`INSERT OR IGNORE INTO keywords (recipe_id, keyword) VALUES (?, ?)`,
			recipeID, kw); err != nil {
			return err
		}
	}
	return nil
}

func ListCategories(db *sql.DB) ([]CategoryCount, error) {
	rows, err := db.Query(`SELECT category, COUNT(*) FROM recipes WHERE category != '' GROUP BY category ORDER BY category`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cats []CategoryCount
	for rows.Next() {
		var c CategoryCount
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

func ListKeywords(db *sql.DB) ([]KeywordCount, error) {
	rows, err := db.Query(`SELECT keyword, COUNT(*) FROM keywords GROUP BY keyword ORDER BY keyword`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var kws []KeywordCount
	for rows.Next() {
		var k KeywordCount
		if err := rows.Scan(&k.Keyword, &k.Count); err != nil {
			return nil, err
		}
		kws = append(kws, k)
	}
	return kws, rows.Err()
}
