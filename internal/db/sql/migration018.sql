CREATE TABLE IF NOT EXISTS activities (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	user_id TEXT NOT NULL,
	actor_id TEXT NOT NULL DEFAULT '',
	app TEXT NOT NULL,
	type TEXT NOT NULL,
	subject TEXT NOT NULL,
	subject_template TEXT NOT NULL DEFAULT '',
	subject_params TEXT NOT NULL DEFAULT '{}',
	object_type TEXT NOT NULL DEFAULT '',
	object_id TEXT NOT NULL DEFAULT '',
	object_name TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_activities_user ON activities(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_activities_object ON activities(object_type, object_id);
