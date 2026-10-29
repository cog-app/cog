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
