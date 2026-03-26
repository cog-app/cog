-- IF COLUMN users.totp_secret NOT EXIST
ALTER TABLE users ADD COLUMN totp_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN mfa_method TEXT NOT NULL DEFAULT '';

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
