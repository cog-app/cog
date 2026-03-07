CREATE TABLE IF NOT EXISTS deleted_tasks (
	list_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	deleted_at_token INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS lists (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	uid TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT 'Tasks',
	color TEXT NOT NULL DEFAULT '#0082c9',
	sort_order INTEGER NOT NULL DEFAULT 0,
	ctag INTEGER NOT NULL DEFAULT 1,
	sync_token INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS tasks (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	list_id INTEGER NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
	uid TEXT NOT NULL,
	etag TEXT NOT NULL DEFAULT '',
	parent_id INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
	summary TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	priority INTEGER NOT NULL DEFAULT 0,
	percent_complete INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'NEEDS-ACTION',
	completed_at INTEGER,
	due_at INTEGER,
	start_at INTEGER,
	sort_order INTEGER NOT NULL DEFAULT 0,
	ics_data TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(list_id, uid)
);

CREATE INDEX IF NOT EXISTS idx_deleted_tasks_list ON deleted_tasks(list_id, deleted_at_token);

CREATE INDEX IF NOT EXISTS idx_tasks_due ON tasks(due_at);

CREATE INDEX IF NOT EXISTS idx_tasks_list_sort ON tasks(list_id, sort_order);

CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_id);

CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);
