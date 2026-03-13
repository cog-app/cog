CREATE TABLE IF NOT EXISTS keywords (
    recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    keyword TEXT NOT NULL,
    PRIMARY KEY (recipe_id, keyword)
);

CREATE TABLE IF NOT EXISTS recipe_ingredients (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS recipe_instructions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    text TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS recipes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    source_url TEXT NOT NULL DEFAULT '',
    image_path TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL DEFAULT '',
    cuisine TEXT NOT NULL DEFAULT '',
    recipe_yield TEXT NOT NULL DEFAULT '',
    prep_time TEXT NOT NULL DEFAULT '',
    cook_time TEXT NOT NULL DEFAULT '',
    total_time TEXT NOT NULL DEFAULT '',
    calories TEXT NOT NULL DEFAULT '',
    json_ld TEXT NOT NULL DEFAULT '{}',
    etag TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_ingredients_recipe ON recipe_ingredients(recipe_id);

CREATE INDEX IF NOT EXISTS idx_instructions_recipe ON recipe_instructions(recipe_id);

CREATE INDEX IF NOT EXISTS idx_keywords_keyword ON keywords(keyword);

CREATE INDEX IF NOT EXISTS idx_recipes_category ON recipes(category);

CREATE INDEX IF NOT EXISTS idx_recipes_name ON recipes(name);

CREATE INDEX IF NOT EXISTS idx_recipes_updated ON recipes(updated_at DESC);
