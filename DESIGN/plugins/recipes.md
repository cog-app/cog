# Recipe Book Plugin Design

The Recipe Book plugin (`id: recipes`) provides recipe management with categories, keyword tags, URL-based import (schema.org JSON-LD extraction), ingredient search, and NextCloud Cookbook API compatibility.

## 1. Role and Loading Model

- **Plugin Type:** 1st-party plugin.
- Uses only the public plugin interface.
- **Storage:** `app_data/recipes/plugin.db` per user.
- **Image Files:** Recipe images stored under `app_data/recipes/images/` in the user's data directory, keyed by recipe ID.

## 2. Data Model

### Recipes

Core content records. The `json_ld` column stores the full schema.org Recipe JSON-LD as received or generated, serving as the canonical representation for API responses and export. Denormalized columns exist for efficient SQL filtering and sorting without parsing JSON on every query.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `name` | TEXT NOT NULL | schema.org `name` |
| `description` | TEXT NOT NULL DEFAULT '' | schema.org `description` |
| `source_url` | TEXT NOT NULL DEFAULT '' | Original URL the recipe was imported from |
| `image_path` | TEXT NOT NULL DEFAULT '' | Relative path under `app_data/recipes/images/` |
| `category` | TEXT NOT NULL DEFAULT '' | Single category (schema.org `recipeCategory`) |
| `cuisine` | TEXT NOT NULL DEFAULT '' | schema.org `recipeCuisine` |
| `recipe_yield` | TEXT NOT NULL DEFAULT '' | e.g. "4 servings" |
| `prep_time` | TEXT NOT NULL DEFAULT '' | ISO 8601 duration (e.g. "PT15M") |
| `cook_time` | TEXT NOT NULL DEFAULT '' | ISO 8601 duration |
| `total_time` | TEXT NOT NULL DEFAULT '' | ISO 8601 duration |
| `calories` | TEXT NOT NULL DEFAULT '' | From `nutrition.calories` |
| `json_ld` | TEXT NOT NULL DEFAULT '{}' | Full schema.org Recipe JSON-LD |
| `etag` | TEXT NOT NULL DEFAULT '' | Optimistic concurrency |
| `created_at` | TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) | |
| `updated_at` | TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')) | |

Resource Type: `recipes:recipe`.

### Recipe Ingredients

Denormalized ingredient lines for search indexing and template rendering.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `recipe_id` | INTEGER NOT NULL | FK → recipes(id) ON DELETE CASCADE |
| `sort_order` | INTEGER NOT NULL DEFAULT 0 | |
| `text` | TEXT NOT NULL | e.g. "2 cups flour" |

### Recipe Instructions

Denormalized instruction steps.

| Column | Type | Notes |
|--------|------|-------|
| `id` | INTEGER PK | |
| `recipe_id` | INTEGER NOT NULL | FK → recipes(id) ON DELETE CASCADE |
| `sort_order` | INTEGER NOT NULL DEFAULT 0 | |
| `text` | TEXT NOT NULL | Step text (from HowToStep) |

### Keywords

Many-to-many recipe/keyword mapping. Keywords serve as both NextCloud Cookbook `keywords` and user-facing meal-type tags (breakfast, dinner, dessert, kid friendly, etc.).

| Column | Type | Notes |
|--------|------|-------|
| `recipe_id` | INTEGER NOT NULL | FK → recipes(id) ON DELETE CASCADE |
| `keyword` | TEXT NOT NULL | Tag string, case-preserved |

PK: `(recipe_id, keyword)`.

### Schema

```sql
CREATE TABLE recipes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    source_url   TEXT NOT NULL DEFAULT '',
    image_path   TEXT NOT NULL DEFAULT '',
    category     TEXT NOT NULL DEFAULT '',
    cuisine      TEXT NOT NULL DEFAULT '',
    recipe_yield TEXT NOT NULL DEFAULT '',
    prep_time    TEXT NOT NULL DEFAULT '',
    cook_time    TEXT NOT NULL DEFAULT '',
    total_time   TEXT NOT NULL DEFAULT '',
    calories     TEXT NOT NULL DEFAULT '',
    json_ld      TEXT NOT NULL DEFAULT '{}',
    etag         TEXT NOT NULL DEFAULT '',
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX idx_recipes_category ON recipes(category);
CREATE INDEX idx_recipes_name ON recipes(name);
CREATE INDEX idx_recipes_updated ON recipes(updated_at DESC);

CREATE TABLE recipe_ingredients (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    recipe_id  INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    text       TEXT NOT NULL
);
CREATE INDEX idx_ingredients_recipe ON recipe_ingredients(recipe_id);

CREATE TABLE recipe_instructions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    recipe_id  INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    text       TEXT NOT NULL
);
CREATE INDEX idx_instructions_recipe ON recipe_instructions(recipe_id);

CREATE TABLE keywords (
    recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    keyword   TEXT NOT NULL,
    PRIMARY KEY (recipe_id, keyword)
);
CREATE INDEX idx_keywords_keyword ON keywords(keyword);
```

## 3. Import Pipeline

### URL Import (Primary Method)

The URL import flow mirrors NextCloud Cookbook: the user provides a URL, the server fetches the page, and extracts structured recipe data.

1. **Fetch**: HTTP GET with a browser-like `User-Agent`, 30-second timeout, redirect limit of 5 hops, `http`/`https` only. Response body capped at 5 MB.
2. **Extract**: Parse the HTML and find `<script type="application/ld+json">` tags. Decode each as JSON. Handle three cases:
   - Single `Recipe` object (`@type: "Recipe"`).
   - `@graph` array containing a `Recipe` object.
   - Array of objects, one of which has `@type: "Recipe"`.
3. **Normalize**: Map the extracted JSON-LD fields to the `recipes` table columns. Parse `recipeIngredient` array into `recipe_ingredients` rows. Parse `recipeInstructions` (array of strings or `HowToStep` objects) into `recipe_instructions` rows. Extract keywords from the `keywords` field (comma-separated string or array). Store the full JSON-LD blob in `json_ld`.
4. **Image**: If the JSON-LD contains an `image` field (URL or array of URLs), fetch the first image (same HTTP client constraints), store it under `app_data/recipes/images/{recipe_id}.{ext}`, and set `image_path`.
5. **Store**: Insert the recipe, ingredients, instructions, and keywords in a single transaction. Record the original `source_url` for attribution.

### JSON-LD Direct Import

Accept a raw JSON-LD payload (paste or file upload) and run steps 3–5 above. Validate that `@type` is `Recipe`.

### Reindex

`POST /api/v1/reindex` re-reads all `json_ld` columns and rebuilds the denormalized columns and child rows. Recovery/maintenance operation, processed in bounded batches.

## 4. API and Routing

### NextCloud Cookbook Compatibility API

JSON endpoints for NextCloud Cookbook client compatibility.

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `api/recipes` | List all recipes (summary: id, name, keywords, category, dateCreated, dateModified) |
| `POST` | `api/recipes` | Create recipe from JSON-LD body |
| `GET` | `api/recipes/{id}` | Get full recipe JSON-LD |
| `PUT` | `api/recipes/{id}` | Update recipe from JSON-LD body |
| `DELETE` | `api/recipes/{id}` | Delete recipe |
| `GET` | `api/recipes/{id}/image` | Get recipe image (full size) |
| `PUT` | `api/recipes/{id}/image` | Upload recipe image |
| `DELETE` | `api/recipes/{id}/image` | Delete recipe image |
| `GET` | `api/categories` | List categories with recipe counts |
| `PUT` | `api/categories/{name}` | Rename category |
| `GET` | `api/categories/{name}` | List recipes in category |
| `GET` | `api/keywords` | List all keywords with recipe counts |
| `POST` | `api/reindex` | Rebuild denormalized data from json_ld |
| `POST` | `api/import` | Import recipe from URL (body: `{"url":"..."}`) |

### Plugin UI Pages (GET, HTML)

| Path | Description |
|------|-------------|
| `{$}` | Recipe grid/list with category sidebar, keyword tag chips, and search box |
| `category/{name}` | Recipes filtered by category |
| `keyword/{name}` | Recipes filtered by keyword tag |
| `recipe/{id}` | Full recipe detail (ingredients, instructions, timing, nutrition, tags, source link) |
| `recipe/{id}/edit` | Recipe editor form with tag autocomplete |
| `new` | New recipe form (manual entry) |
| `import` | Import from URL dialog |

### Plugin UI Actions (POST/PUT/DELETE, CSRF-protected)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `recipes` | Create recipe from form → redirect to detail |
| `POST` | `recipes/{id}` | Update recipe from form → redirect to detail |
| `DELETE` | `recipes/{id}` | Delete recipe → redirect to list |
| `POST` | `recipes/import-url` | Import recipe from URL → redirect to detail |
| `POST` | `recipes/{id}/image` | Upload recipe image → redirect to detail |
| `DELETE` | `recipes/{id}/image` | Delete recipe image → redirect to detail |

### Nav Item

- **Label:** "Recipes"
- **Icon:** utensils SVG
- **Path:** `/apps/recipes`

### Dashboard Widget

- Shows recently added recipes (name, category, image thumbnail).
- Priority: 40.

## 5. Sharing and Resources

- Shareable resources:
  - `recipes:recipe` — individual recipes.
- Permissions:
  - `READ`: view recipe.
  - `WRITE`: edit recipe.
  - `SHARE`: reshare.
- Public links supported via Cog's share system (token, optional password, expiration). Public link renders a read-only recipe detail page.
- Resolution uses global `shares` + owner DB scoped query enforcement.

## 6. Search Indexing

Register a `SearchSourceDef` for the `recipes` table.

Enrichment SQL:

```sql
SELECT r.rowid, r.name,
       r.category || CASE WHEN k.tags != '' THEN ' · ' || k.tags ELSE '' END AS subtext,
       r.description
FROM recipes r
LEFT JOIN (
    SELECT recipe_id, GROUP_CONCAT(keyword, ', ') AS tags
    FROM keywords GROUP BY recipe_id
) k ON k.recipe_id = r.id
WHERE r.rowid IN (?)
```

Mapping:
- `name`: recipe name (primary search result title).
- `subtext`: category + keyword tags (secondary line in results).
- `content`: description text.

`ContentFunc`: concatenate all ingredient texts and instruction texts from child tables. This enables searching recipes by ingredient (e.g. searching "chicken" finds recipes with chicken in the ingredient list).

Dependency mapping: changes to `keywords`, `recipe_ingredients`, or `recipe_instructions` tables trigger re-index of the parent recipe.

## 7. Performance and Maintenance

- Index `category`, `name`, `updated_at DESC`, and `keyword` for fast listing and filtering.
- Recipe list endpoints return only summary fields (no `json_ld` blob) to avoid transferring large JSON on list views.
- Category and keyword list queries use `GROUP BY` with counts, hitting indexes directly.
- Image fetch during URL import is bounded: 5 MB max response body, 30-second timeout.
- Reindex operation processes recipes in bounded batches to avoid long write transactions.
- Pagination enforced on all list endpoints (default 20, max 100).

## 8. Security

- **URL Import**: Restrict outbound fetch to `http`/`https` protocols. Enforce redirect hop limit (5). Cap response body size (5 MB). Use a dedicated `http.Client` with timeouts (same pattern as RSS plugin). Reject `javascript:`, `data:`, `file:`, and other non-HTTP schemes.
- **HTML Sanitization**: Recipe descriptions, ingredient text, and instruction text must be sanitized before HTML rendering to prevent XSS from imported content.
- **Image Validation**: Validate fetched images by content type (`image/jpeg`, `image/png`, `image/webp`). Do not serve user-uploaded images with arbitrary content types.
- **CSRF**: Enforced on all state-changing page/action routes via `RegisterAction`.
- **JSON-LD Validation**: Validate `@type` is `Recipe`. Reject payloads exceeding 1 MB. Do not trust arbitrary fields for filenames or paths.
- **Rate Limiting**: Rate-limit the URL import endpoint to prevent outbound HTTP amplification abuse.

## 9. Import/Export

- **URL Import**: `POST /apps/recipes/recipes/import-url` (web UI) and `POST /api/recipes/import` (API). Extracts schema.org Recipe JSON-LD from any web page containing structured data.
- **JSON-LD Import**: `POST /api/recipes/recipes` accepts raw Recipe JSON-LD (same as NextCloud Cookbook create endpoint).
- **JSON-LD Export**: `GET /api/recipes/recipes/{id}` returns the full schema.org Recipe JSON-LD. Clients can iterate the list endpoint to export all recipes.

## 10. Keyword Tags for Meal Types

Keywords are free-form tags stored in the `keywords` table. They map directly to the NextCloud Cookbook `keywords` field and are used for meal-type classification.

### Suggested Tags (UI Presets)

The recipe editor presents these as quick-add suggestions, but users can create any tag:

- **Meal type**: breakfast, lunch, dinner, snack, dessert
- **Audience**: kid friendly, crowd pleaser, date night
- **Effort**: quick, easy, meal prep, one pot
- **Diet**: vegetarian, vegan, gluten free, dairy free, low carb, keto

### Filtering

- Recipe list page shows all used keywords as clickable tag chips.
- Clicking a tag filters the list to recipes with that keyword.
- Multiple tags can be combined (AND logic) via query parameters: `/apps/recipes?keyword=dinner&keyword=quick`.
- API: `GET /api/recipes/recipes?keywords=dinner,quick` for Cookbook client filtering.

## 11. File Layout

```
plugin/recipes/
    recipes.go        Plugin struct, ID, Register, Start, Stop
    store.go          CRUD operations for recipes, ingredients, instructions, keywords
    import.go         URL fetch, HTML parse, JSON-LD extraction, normalization
    pages.go          HTTP page handlers
    pages.templ       Page templates (list, detail, edit, import)
    widget.templ      Dashboard widget template
    cookbookapi.go    NextCloud Cookbook compatibility API handlers
    sql/
        schema.sql    Database schema
```
