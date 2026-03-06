CREATE TABLE IF NOT EXISTS calendars (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	uid TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT 'Personal',
	color TEXT NOT NULL DEFAULT '#0082c9',
	description TEXT NOT NULL DEFAULT '',
	sort_order INTEGER NOT NULL DEFAULT 0,
	ctag INTEGER NOT NULL DEFAULT 1,
	sync_token INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS deleted_events (
	calendar_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	deleted_at_token INTEGER NOT NULL
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
	calendar_id INTEGER NOT NULL REFERENCES calendars(id) ON DELETE CASCADE,
	uid TEXT NOT NULL,
	etag TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	location TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	dtstart INTEGER NOT NULL DEFAULT 0,
	dtend INTEGER NOT NULL DEFAULT 0,
	is_all_day INTEGER NOT NULL DEFAULT 0,
	ics_data TEXT NOT NULL,
	color TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(calendar_id, uid)
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

CREATE INDEX IF NOT EXISTS idx_deleted_events_cal ON deleted_events(calendar_id, deleted_at_token);

CREATE INDEX IF NOT EXISTS idx_events_cal_start ON events(calendar_id, dtstart);

CREATE INDEX IF NOT EXISTS idx_events_calendar ON events(calendar_id);

CREATE INDEX IF NOT EXISTS idx_events_dtstart ON events(dtstart);

CREATE INDEX IF NOT EXISTS idx_notifications_fire ON notifications(fired, trigger_at);
