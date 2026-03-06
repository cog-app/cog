CREATE TABLE IF NOT EXISTS feeds (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL,
	url TEXT NOT NULL UNIQUE,
	site_url TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	favicon_url TEXT NOT NULL DEFAULT '',
	etag TEXT NOT NULL DEFAULT '',
	last_modified TEXT NOT NULL DEFAULT '',
	poll_interval_sec INTEGER NOT NULL DEFAULT 1800,
	next_check_at INTEGER NOT NULL DEFAULT 0,
	last_checked_at INTEGER NOT NULL DEFAULT 0,
	last_success_at INTEGER NOT NULL DEFAULT 0,
	error_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	paused INTEGER NOT NULL DEFAULT 0,
	auto_read_pattern TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS folders (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	sort_order INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS items (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
	guid TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	content_html TEXT NOT NULL DEFAULT '',
	published_at INTEGER NOT NULL DEFAULT 0,
	fetched_at INTEGER NOT NULL DEFAULT (unixepoch()),
	is_read INTEGER NOT NULL DEFAULT 0,
	is_starred INTEGER NOT NULL DEFAULT 0,
	content_hash TEXT NOT NULL DEFAULT '',
	UNIQUE(feed_id, guid)
);

CREATE INDEX IF NOT EXISTS idx_feeds_next_check ON feeds(next_check_at);

CREATE INDEX IF NOT EXISTS idx_items_feed_id ON items(feed_id);

CREATE INDEX IF NOT EXISTS idx_items_starred ON items(is_starred, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_items_unread ON items(is_read, published_at DESC);
