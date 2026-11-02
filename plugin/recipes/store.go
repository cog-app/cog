package recipes

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// execer is satisfied by *sql.DB and *sql.Tx, so the write helpers below can
// run on their own or as part of a transaction that saves a whole recipe.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// inTx runs fn in a transaction, committing if it succeeds. A recipe and its
// ingredients, steps and tags are saved together so that a failure part way
// through does not leave a recipe with half its lists.
func inTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

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

func CreateRecipe(db execer, r *Recipe) (int64, error) {
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

func UpdateRecipe(db execer, r *Recipe) error {
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

func SetIngredients(db execer, recipeID int64, texts []string) error {
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

func SetInstructions(db execer, recipeID int64, texts []string) error {
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

// KeywordsByRecipe returns every recipe's keywords, sorted, keyed by recipe
// id, in one query.
func KeywordsByRecipe(db *sql.DB) (map[int64][]string, error) {
	rows, err := db.Query(`SELECT recipe_id, keyword FROM keywords ORDER BY recipe_id, keyword`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kws := make(map[int64][]string)
	for rows.Next() {
		var id int64
		var kw string
		if err := rows.Scan(&id, &kw); err != nil {
			return nil, err
		}
		kws[id] = append(kws[id], kw)
	}
	return kws, rows.Err()
}

func SetKeywords(db execer, recipeID int64, keywords []string) error {
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

// draftTTL is how long a saved form waits for the page that refills it.
const draftTTL = 3600 // seconds

// SaveDraft keeps form, the fields of a recipe form a save rejected, until
// TakeDraft reads it. Drafts left unread past draftTTL are removed on the way.
func SaveDraft(db *sql.DB, form map[string]string) (int64, error) {
	data, err := json.Marshal(form)
	if err != nil {
		return 0, fmt.Errorf("encode draft: %w", err)
	}
	var id int64
	err = inTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM recipe_drafts WHERE created_at < unixepoch() - ?`, draftTTL); err != nil {
			return fmt.Errorf("expire drafts: %w", err)
		}
		res, err := tx.Exec(`INSERT INTO recipe_drafts (form) VALUES (?)`, string(data))
		if err != nil {
			return fmt.Errorf("save draft: %w", err)
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// TakeDraft returns the form SaveDraft kept under id and removes it, or nil
// when there is none (already taken, or expired).
func TakeDraft(db *sql.DB, id int64) (map[string]string, error) {
	var data string
	err := db.QueryRow(`DELETE FROM recipe_drafts WHERE id = ? AND created_at >= unixepoch() - ? RETURNING form`, id, draftTTL).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("take draft: %w", err)
	}
	var form map[string]string
	if err := json.Unmarshal([]byte(data), &form); err != nil {
		return nil, fmt.Errorf("decode draft: %w", err)
	}
	return form, nil
}
