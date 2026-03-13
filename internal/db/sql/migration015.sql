CREATE TABLE shares_new (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT NOT NULL REFERENCES users(id),
	resource_type TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	subject_type TEXT NOT NULL CHECK(subject_type IN ('USER', 'GROUP', 'PUBLIC', 'INSTANCE')),
	subject_id TEXT NOT NULL DEFAULT '',
	permissions INTEGER NOT NULL DEFAULT 1,
	token TEXT,
	password_hash TEXT,
	expiration INTEGER,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
INSERT INTO shares_new SELECT * FROM shares;
DROP TABLE shares;
ALTER TABLE shares_new RENAME TO shares;
CREATE INDEX IF NOT EXISTS idx_shares_subject ON shares(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_shares_resource ON shares(resource_type, resource_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_shares_token ON shares(token) WHERE token IS NOT NULL;
