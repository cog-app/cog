CREATE TABLE IF NOT EXISTS volume_quotas (
	id TEXT PRIMARY KEY,
	volume_id TEXT NOT NULL REFERENCES volumes(id) ON DELETE CASCADE,
	subject_type TEXT NOT NULL CHECK(subject_type IN ('USER', 'GROUP', 'DEFAULT')),
	subject_id TEXT NOT NULL DEFAULT '',
	quota_bytes INTEGER NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(volume_id, subject_type, subject_id)
);
