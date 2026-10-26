CREATE TABLE IF NOT EXISTS users (
	id TEXT PRIMARY KEY,
	username TEXT NOT NULL UNIQUE,
	password_hash TEXT NOT NULL,
	home_mount TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	is_admin INTEGER NOT NULL DEFAULT 0,
	display_name TEXT NOT NULL DEFAULT '',
	avatar TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	avatar_updated_at INTEGER NOT NULL DEFAULT 0,
	totp_secret TEXT NOT NULL DEFAULT '',
	mfa_method TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS app_passwords (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	name TEXT NOT NULL,
	token_hash TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS groups (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	parent_group_id TEXT REFERENCES groups(id),
	owner_id TEXT REFERENCES users(id)
);

CREATE TABLE IF NOT EXISTS group_members (
	group_id TEXT NOT NULL REFERENCES groups(id),
	member_id TEXT NOT NULL,
	member_type TEXT NOT NULL CHECK(member_type IN ('USER', 'OWNER', 'GROUP')),
	PRIMARY KEY (group_id, member_id)
);
CREATE INDEX IF NOT EXISTS idx_group_members_member ON group_members(member_id);

CREATE TABLE IF NOT EXISTS shares (
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
CREATE INDEX IF NOT EXISTS idx_shares_subject ON shares(subject_type, subject_id);
CREATE INDEX IF NOT EXISTS idx_shares_owner ON shares(owner_user_id);
CREATE INDEX IF NOT EXISTS idx_shares_resource ON shares(resource_type, resource_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_shares_token ON shares(token) WHERE token IS NOT NULL;

CREATE TABLE IF NOT EXISTS volumes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	description TEXT NOT NULL DEFAULT '',
	uri TEXT NOT NULL,
	created_by TEXT NOT NULL DEFAULT 'system',
	created_at INTEGER NOT NULL,
	owner_id TEXT REFERENCES users(id),
	config TEXT NOT NULL DEFAULT '{}',
	owner_type TEXT NOT NULL DEFAULT 'user'
);

CREATE TABLE IF NOT EXISTS mounts (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	user_path TEXT NOT NULL,
	volume_id TEXT NOT NULL REFERENCES volumes(id),
	volume_subpath TEXT NOT NULL DEFAULT '',
	read_only INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	UNIQUE(user_id, user_path)
);
CREATE INDEX IF NOT EXISTS idx_mounts_user ON mounts(user_id);
CREATE INDEX IF NOT EXISTS idx_mounts_volume ON mounts(volume_id);

CREATE TABLE IF NOT EXISTS system_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS plugin_settings (
	plugin_id TEXT PRIMARY KEY,
	enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS group_membership_closure (
	ancestor_group_id TEXT NOT NULL,
	descendant_user_id TEXT NOT NULL,
	depth INTEGER NOT NULL,
	PRIMARY KEY (ancestor_group_id, descendant_user_id)
);

CREATE TABLE IF NOT EXISTS group_invitations (
	id TEXT PRIMARY KEY,
	group_id TEXT NOT NULL REFERENCES groups(id),
	email TEXT NOT NULL DEFAULT '',
	token TEXT NOT NULL UNIQUE,
	invited_by TEXT NOT NULL REFERENCES users(id),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	member_type TEXT NOT NULL DEFAULT 'USER'
);
CREATE INDEX IF NOT EXISTS idx_group_invitations_token ON group_invitations(token);

CREATE TABLE IF NOT EXISTS totp_backup_codes (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	code_hash TEXT NOT NULL,
	used INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_totp_backup_user ON totp_backup_codes(user_id);

CREATE TABLE IF NOT EXISTS mfa_pending (
	token TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	next_url TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	credential_data TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webauthn_user ON webauthn_credentials(user_id);

CREATE TABLE IF NOT EXISTS webauthn_sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	session_data TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS email_otp (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL,
	code_hash TEXT NOT NULL,
	expires_at INTEGER NOT NULL,
	used INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS password_reset_tokens (
	token TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	used INTEGER NOT NULL DEFAULT 0
);

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
/* email index for forgot-password lookups */
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email != '';
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
