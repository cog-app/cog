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

CREATE TABLE IF NOT EXISTS event_alarms (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES vevents(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	action TEXT NOT NULL DEFAULT '',
	-- A duration, or a UTC date-time when absolute.
	trigger TEXT NOT NULL DEFAULT '',
	related TEXT NOT NULL DEFAULT '',
	absolute INTEGER NOT NULL DEFAULT 0,
	repeat INTEGER,
	repeat_duration TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	leftovers TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS event_attachments (
	-- ATTACH: a URI, a managed attachment (a URI Cog serves, RFC 8607), or
	-- inline data kept as the base64 text sent.
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES vevents(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	inline INTEGER NOT NULL DEFAULT 0,
	managed_id TEXT NOT NULL DEFAULT '',
	fmttype TEXT NOT NULL DEFAULT '',
	size INTEGER,
	filename TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS event_attendees (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES vevents(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	address TEXT NOT NULL,
	-- The lower-cased mailto address, for lookups; '' for other schemes.
	email TEXT NOT NULL DEFAULT '',
	cn TEXT NOT NULL DEFAULT '',
	partstat TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	rsvp TEXT NOT NULL DEFAULT '',
	cutype TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS events (
	-- One CalDAV resource: a UID's events in one calendar. The iCalendar text
	-- is generated from these rows on read (internal/dav/ical).
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	-- Always VEVENT; the column is in every plugin's objects table
	-- (internal/dav/icalsql).
	component TEXT NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	-- Length of the text served for this version, for getcontentlength.
	size INTEGER NOT NULL DEFAULT 0,
	-- Cog's own colour for the event; not part of the iCalendar data.
	color TEXT NOT NULL DEFAULT '',
	-- When the first instance starts and the last ends; range_end_utc is NULL
	-- when the recurrence has no end. Range queries filter on these, then
	-- expand in Go.
	start_utc INTEGER,
	range_end_utc INTEGER,
	-- VCALENDAR properties other than VERSION, and components that are
	-- neither events nor time zones (JSON, contentline types).
	props TEXT NOT NULL DEFAULT '[]',
	comps TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (collection_id, uri),
	UNIQUE (collection_id, uid)
);

CREATE TABLE IF NOT EXISTS managed_attachments (
	-- This user's managed attachments (RFC 8607). The bytes are a file under
	-- the plugin's data directory named by their SHA-256 (hash), so the same
	-- bytes are stored once. A row lives while an ATTACH in this database
	-- names it.
	managed_id TEXT PRIMARY KEY,
	hash TEXT NOT NULL,
	size INTEGER NOT NULL,
	fmttype TEXT NOT NULL DEFAULT '',
	filename TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS notifications (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	trigger_at INTEGER NOT NULL,
	fired INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS schedule_inbox (
	-- The user's schedule inbox (RFC 6638): iTIP messages delivered to them,
	-- as the text Cog wrote, versioned like a calendar whose collection ID is
	-- 0 (dav.InboxID).
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	size INTEGER NOT NULL,
	method TEXT NOT NULL,
	data TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	UNIQUE (collection_id, uri)
);

CREATE TABLE IF NOT EXISTS vevents (
	-- One VEVENT: the master (recurrence_id_local = '') or an override of one
	-- occurrence. Times are wall time plus zone; *_utc are derived. The
	-- columns are the same in every plugin that stores calendar components
	-- (internal/dav/icalsql); the task ones stay empty here.
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	object_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	recurrence_id_local TEXT NOT NULL DEFAULT '',
	recurrence_id_tzid TEXT NOT NULL DEFAULT '',
	recurrence_id_is_date INTEGER NOT NULL DEFAULT 0,
	this_and_future INTEGER NOT NULL DEFAULT 0,
	summary TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	location TEXT NOT NULL DEFAULT '',
	url TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT '',
	class TEXT NOT NULL DEFAULT '',
	transp TEXT NOT NULL DEFAULT '',
	priority INTEGER,
	sequence INTEGER,
	percent_complete INTEGER,
	dtstart_local TEXT NOT NULL DEFAULT '',
	dtstart_tzid TEXT NOT NULL DEFAULT '',
	dtstart_is_date INTEGER NOT NULL DEFAULT 0,
	dtstart_utc INTEGER,
	dtend_local TEXT NOT NULL DEFAULT '',
	dtend_tzid TEXT NOT NULL DEFAULT '',
	dtend_is_date INTEGER NOT NULL DEFAULT 0,
	due_local TEXT NOT NULL DEFAULT '',
	due_tzid TEXT NOT NULL DEFAULT '',
	due_is_date INTEGER NOT NULL DEFAULT 0,
	due_utc INTEGER,
	-- End of this component's own span, from DTEND, DUE or DURATION.
	end_utc INTEGER,
	duration TEXT NOT NULL DEFAULT '',
	-- COMPLETED as JSON, and its instant.
	completed TEXT NOT NULL DEFAULT '',
	completed_utc INTEGER,
	rrule TEXT NOT NULL DEFAULT '',
	-- RDATE and EXDATE values, DTSTAMP, CREATED and LAST-MODIFIED, ORGANIZER,
	-- CATEGORIES and leftovers, as JSON (internal/dav/ical types).
	rdates TEXT NOT NULL DEFAULT '[]',
	exdates TEXT NOT NULL DEFAULT '[]',
	dtstamp TEXT NOT NULL DEFAULT '',
	created TEXT NOT NULL DEFAULT '',
	last_modified TEXT NOT NULL DEFAULT '',
	organizer TEXT NOT NULL DEFAULT '',
	categories TEXT NOT NULL DEFAULT '[]',
	-- X-APPLE-SORT-ORDER.
	sort_order INTEGER,
	leftovers TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS vtimezones (
	-- Clients' own VTIMEZONEs, for TZIDs that name no zone Cog knows (JSON,
	-- contentline component). Zones Cog knows are generated on output.
	collection_id INTEGER NOT NULL,
	tzid TEXT NOT NULL,
	vtimezone TEXT NOT NULL,
	PRIMARY KEY (collection_id, tzid)
);

CREATE INDEX IF NOT EXISTS idx_alarms_vevent ON event_alarms(component_id);

CREATE INDEX IF NOT EXISTS idx_attachments_managed ON event_attachments(managed_id);

CREATE INDEX IF NOT EXISTS idx_attachments_vevent ON event_attachments(component_id);

CREATE INDEX IF NOT EXISTS idx_attendees_email ON event_attendees(email);

CREATE INDEX IF NOT EXISTS idx_attendees_vevent ON event_attendees(component_id);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_events_collection_version ON events(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_events_range ON events(collection_id, start_utc);

CREATE INDEX IF NOT EXISTS idx_managed_attachments_hash ON managed_attachments(hash);

CREATE INDEX IF NOT EXISTS idx_notifications_event ON notifications(event_id);

CREATE INDEX IF NOT EXISTS idx_notifications_fire ON notifications(fired, trigger_at);

CREATE INDEX IF NOT EXISTS idx_schedule_inbox_created ON schedule_inbox(collection_id, created_at);

CREATE INDEX IF NOT EXISTS idx_schedule_inbox_version ON schedule_inbox(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_vevents_event ON vevents(object_id);
