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

CREATE INDEX IF NOT EXISTS idx_attendees_event ON event_attendees(event_id);
CREATE INDEX IF NOT EXISTS idx_attendees_user ON event_attendees(cog_user);
