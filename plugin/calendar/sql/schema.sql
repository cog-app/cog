CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE TABLE IF NOT EXISTS event_attendees (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	cog_user TEXT NOT NULL DEFAULT '',
	display_name TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'NEEDS-ACTION',
	role TEXT NOT NULL DEFAULT 'REQ-PARTICIPANT',
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS events (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	location TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	dtstart INTEGER NOT NULL DEFAULT 0,
	dtend INTEGER NOT NULL DEFAULT 0,
	is_all_day INTEGER NOT NULL DEFAULT 0,
	color TEXT NOT NULL DEFAULT '',
	ics_data TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(collection_id, uri),
	UNIQUE(collection_id, uid)
);

CREATE TABLE IF NOT EXISTS notifications (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	trigger_at INTEGER NOT NULL,
	fired INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_attendees_event ON event_attendees(event_id);

CREATE INDEX IF NOT EXISTS idx_attendees_user ON event_attendees(cog_user);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_events_collection_version ON events(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_events_dtstart ON events(dtstart);

CREATE INDEX IF NOT EXISTS idx_notifications_event ON notifications(event_id);

CREATE INDEX IF NOT EXISTS idx_notifications_fire ON notifications(fired, trigger_at);
