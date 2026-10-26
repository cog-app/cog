CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE TABLE IF NOT EXISTS journals (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	ics_data TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(collection_id, uri),
	UNIQUE(collection_id, uid)
);

CREATE TABLE IF NOT EXISTS tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	parent_id INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
	summary TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	priority INTEGER NOT NULL DEFAULT 0,
	percent_complete INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'NEEDS-ACTION',
	completed_at INTEGER,
	due_at INTEGER,
	start_at INTEGER,
	sort_order INTEGER NOT NULL DEFAULT 0,
	ics_data TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(collection_id, uri),
	UNIQUE(collection_id, uid)
);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_journals_collection_version ON journals(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_tasks_collection_sort ON tasks(collection_id, sort_order);

CREATE INDEX IF NOT EXISTS idx_tasks_collection_version ON tasks(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(due_at);

CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id);
