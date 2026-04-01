CREATE TABLE IF NOT EXISTS episode_tags (
	episode_id INTEGER NOT NULL REFERENCES episodes(id) ON DELETE CASCADE,
	tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
	PRIMARY KEY (episode_id, tag_id)
);

CREATE TABLE IF NOT EXISTS episodes (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	source_id INTEGER NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	guid TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	media_url TEXT NOT NULL DEFAULT '',
	media_type TEXT NOT NULL DEFAULT 'audio' CHECK(media_type IN ('audio', 'video')),
	duration_sec INTEGER NOT NULL DEFAULT 0,
	thumbnail_url TEXT NOT NULL DEFAULT '',
	captions_path TEXT NOT NULL DEFAULT '',
	clean_audio_path TEXT NOT NULL DEFAULT '',
	published_at INTEGER NOT NULL DEFAULT 0,
	fetched_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(source_id, guid)
);

CREATE TABLE IF NOT EXISTS filters (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	sort_order INTEGER NOT NULL DEFAULT 0,
	query TEXT NOT NULL DEFAULT '*',
	action TEXT NOT NULL CHECK(action IN ('skip', 'tag', 'mute_profanity')),
	action_value TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS groups (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE,
	sort_order INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS playback (
	episode_id INTEGER PRIMARY KEY REFERENCES episodes(id) ON DELETE CASCADE,
	progress_sec REAL NOT NULL DEFAULT 0,
	completed INTEGER NOT NULL DEFAULT 0,
	last_played_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS queue (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	episode_id INTEGER NOT NULL UNIQUE REFERENCES episodes(id) ON DELETE CASCADE,
	position INTEGER NOT NULL DEFAULT 0,
	added_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	group_id INTEGER REFERENCES groups(id) ON DELETE SET NULL,
	kind TEXT NOT NULL CHECK(kind IN ('podcast', 'youtube', 'url')),
	url TEXT NOT NULL UNIQUE,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	image_url TEXT NOT NULL DEFAULT '',
	etag TEXT NOT NULL DEFAULT '',
	last_modified TEXT NOT NULL DEFAULT '',
	poll_interval_sec INTEGER NOT NULL DEFAULT 3600,
	next_check_at INTEGER NOT NULL DEFAULT 0,
	last_checked_at INTEGER NOT NULL DEFAULT 0,
	last_success_at INTEGER NOT NULL DEFAULT 0,
	error_count INTEGER NOT NULL DEFAULT 0,
	last_error TEXT NOT NULL DEFAULT '',
	paused INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS tags (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL UNIQUE
);

CREATE INDEX IF NOT EXISTS idx_episodes_published ON episodes(published_at DESC);

CREATE INDEX IF NOT EXISTS idx_episodes_source_id ON episodes(source_id);

CREATE INDEX IF NOT EXISTS idx_filters_order ON filters(sort_order);

CREATE INDEX IF NOT EXISTS idx_playback_last ON playback(last_played_at DESC);

CREATE INDEX IF NOT EXISTS idx_queue_position ON queue(position);

CREATE INDEX IF NOT EXISTS idx_sources_next_check ON sources(next_check_at);
