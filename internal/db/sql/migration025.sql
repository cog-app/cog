-- SSH public keys live in the system database, so a public-key login finds
-- its user with one indexed lookup on the fingerprint instead of opening
-- every user's database. A key is unique per user, not globally: public
-- keys are public, so global uniqueness would let anyone claim another
-- person's key and lock them out of adding it. Keys
-- from the per-user databases are copied in by internal/ssh/keys on first
-- use (ssh.keys_moved in system_settings marks it done).
CREATE TABLE IF NOT EXISTS ssh_keys (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	public_key TEXT NOT NULL,
	fingerprint TEXT NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ssh_keys_user_fingerprint ON ssh_keys(user_id, fingerprint);
