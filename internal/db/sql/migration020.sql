CREATE TABLE IF NOT EXISTS dav_collections (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	owner_user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	home TEXT NOT NULL,
	name TEXT NOT NULL,
	components TEXT NOT NULL,
	display_name TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '',
	sort_order INTEGER NOT NULL DEFAULT 0,
	timezone TEXT NOT NULL DEFAULT '',
	dead_props TEXT NOT NULL DEFAULT '[]',
	props_version INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(owner_user_id, home, name)
);
