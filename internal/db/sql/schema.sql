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
	mfa_method TEXT NOT NULL DEFAULT '',
	totp_last_step INTEGER NOT NULL DEFAULT 0
);

-- An address a user asked to change to, kept until they open the link sent
-- to it. users.email holds only confirmed addresses (or ones an admin set).
CREATE TABLE IF NOT EXISTS email_verifications (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_verifications_user ON email_verifications(user_id);

-- A user's refusal of a share made to a group they are in, or to everyone:
-- it stops reaching them without being taken from the others.
CREATE TABLE IF NOT EXISTS share_declines (
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	PRIMARY KEY (share_id, user_id)
);

-- The usernames of deleted accounts, with the address each last had. Other
-- servers and people still know the account by them, so no new account may
-- take one until an admin releases it.
CREATE TABLE IF NOT EXISTS retired_usernames (
	username TEXT PRIMARY KEY COLLATE NOCASE,
	email TEXT NOT NULL DEFAULT '',
	retired_at INTEGER NOT NULL
);

-- Nextcloud Login Flow v2 sign-ins waiting for their user to grant them.
-- The tokens are stored hashed; the app password a grant mints is sealed
-- (HPKE) to public_key, whose private half only the poll token derives.
CREATE TABLE IF NOT EXISTS login_flows (
	poll_hash TEXT PRIMARY KEY,
	login_hash TEXT NOT NULL UNIQUE,
	public_key BLOB NOT NULL,
	client TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	login_name TEXT NOT NULL DEFAULT '',
	sealed BLOB,
	ip TEXT NOT NULL DEFAULT ''
);

-- One-time links to a user's file for Nextcloud apps' media players. The
-- token is stored hashed; the file is named by ID, so a rename before the
-- link is used does not break it.
CREATE TABLE IF NOT EXISTS direct_tokens (
	token_hash TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	file_id INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);

-- Failed attempts the rate limiters count (internal/ratelimit), kept here
-- so a restart does not forgive them and every replica counts the same.
-- at is in milliseconds.
CREATE TABLE IF NOT EXISTS rate_attempts (
	limiter TEXT NOT NULL,
	key TEXT NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rate_attempts_key ON rate_attempts(limiter, key, at);
CREATE INDEX IF NOT EXISTS idx_rate_attempts_at ON rate_attempts(limiter, at);

CREATE TABLE IF NOT EXISTS sessions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	last_seen_at INTEGER NOT NULL DEFAULT 0,
	user_agent TEXT NOT NULL DEFAULT '',
	ip TEXT NOT NULL DEFAULT '',
	hashed INTEGER NOT NULL DEFAULT 0 -- id is the SHA-256 of the token, not the token
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

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
	member_type TEXT NOT NULL DEFAULT 'USER',
	invitee_id TEXT NOT NULL DEFAULT ''
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
	kind TEXT NOT NULL DEFAULT '',
	UNIQUE(owner_user_id, home, name)
);
/* a remote feed a subscription collection is filled from */
CREATE TABLE IF NOT EXISTS dav_subscriptions (
	collection_id INTEGER PRIMARY KEY REFERENCES dav_collections(id) ON DELETE CASCADE,
	url TEXT NOT NULL,
	etag TEXT NOT NULL DEFAULT '',
	last_modified TEXT NOT NULL DEFAULT '',
	refresh INTEGER NOT NULL DEFAULT 0,
	next_poll_at INTEGER NOT NULL DEFAULT 0,
	last_poll_at INTEGER NOT NULL DEFAULT 0,
	last_success_at INTEGER NOT NULL DEFAULT 0,
	failures INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	skipped INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_dav_subscriptions_next ON dav_subscriptions(next_poll_at);
/* how far the birthday calendar has read each address book */
CREATE TABLE IF NOT EXISTS dav_birthday_cursors (
	collection_id INTEGER NOT NULL REFERENCES dav_collections(id) ON DELETE CASCADE,
	book_id INTEGER NOT NULL,
	version INTEGER NOT NULL,
	book_owner_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (collection_id, book_id)
);
/* push subscriptions: a client's push endpoint and what it watches (WebDAV-Push; JMAP push) */
CREATE TABLE IF NOT EXISTS push_subscriptions (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	protocol TEXT NOT NULL,
	resource TEXT NOT NULL,
	endpoint TEXT NOT NULL,
	p256dh BLOB NOT NULL,
	auth BLOB NOT NULL,
	options TEXT NOT NULL DEFAULT '',
	expires_at INTEGER NOT NULL,
	pending INTEGER NOT NULL DEFAULT 0,
	seq INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL DEFAULT 0,
	failures INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	last_success_at INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE(protocol, resource, endpoint)
);
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_due ON push_subscriptions(next_attempt_at) WHERE pending != 0;
CREATE INDEX IF NOT EXISTS idx_push_subscriptions_expiry ON push_subscriptions(expires_at);

-- SSH public keys, unique per user; login looks up (username, fingerprint).
CREATE TABLE IF NOT EXISTS ssh_keys (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_user_fingerprint ON ssh_keys(user_id, fingerprint);

-- Bytes each plugin keeps in a user's storage (internal/storage), counted
-- toward their quota with their files.
CREATE TABLE IF NOT EXISTS storage_usage (
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	plugin_id TEXT NOT NULL,
	bytes INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (user_id, plugin_id)
);
