CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0
, modified INTEGER NOT NULL DEFAULT 0);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE TABLE IF NOT EXISTS task_alarms (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES task_components(id) ON DELETE CASCADE,
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

CREATE TABLE IF NOT EXISTS task_attendees (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES task_components(id) ON DELETE CASCADE,
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

CREATE TABLE IF NOT EXISTS task_components (
	-- One VTODO or VJOURNAL: the master (recurrence_id_local = '') or an
	-- override of one occurrence. Times are wall time plus zone; *_utc are
	-- derived. The columns are the same in every plugin that stores calendar
	-- components (internal/dav/icalsql); dtend and transp stay empty here.
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	object_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
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

CREATE TABLE IF NOT EXISTS task_relations (
	-- RELATED-TO, by UID: the related component may arrive later, or live
	-- in another list.
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	component_id INTEGER NOT NULL REFERENCES task_components(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	-- RELTYPE as written; '' when absent, which means PARENT.
	reltype TEXT NOT NULL DEFAULT '',
	related_uid TEXT NOT NULL,
	-- Set on the component's first PARENT relation, which the task tree
	-- follows.
	is_parent INTEGER NOT NULL DEFAULT 0,
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS tasks (
	-- One CalDAV resource: a UID's tasks (VTODO) or journal entries
	-- (VJOURNAL) in one collection. The iCalendar text is generated from
	-- these rows on read (internal/dav/ical).
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	component TEXT NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	-- The version that created the row: JMAP's /changes tells resources
	-- created since a state from those only updated.
	created_version INTEGER NOT NULL DEFAULT 0,
	-- Length of the text served for this version, for getcontentlength.
	size INTEGER NOT NULL DEFAULT 0,
	-- When the first instance starts and the last ends; range_end_utc is NULL
	-- when the recurrence has no end.
	start_utc INTEGER,
	range_end_utc INTEGER,
	-- VCALENDAR properties other than VERSION, and components that are
	-- neither of the resource's kind nor time zones (JSON, contentline types).
	props TEXT NOT NULL DEFAULT '[]',
	comps TEXT NOT NULL DEFAULT '[]',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (collection_id, uri),
	UNIQUE (collection_id, uid)
);

CREATE TABLE IF NOT EXISTS vtimezones (
	-- Clients' own VTIMEZONEs, for TZIDs that name no zone Cog knows (JSON,
	-- contentline component). Zones Cog knows are generated on output.
	collection_id INTEGER NOT NULL,
	tzid TEXT NOT NULL,
	vtimezone TEXT NOT NULL,
	PRIMARY KEY (collection_id, tzid)
);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_task_alarms_component ON task_alarms(component_id);

CREATE INDEX IF NOT EXISTS idx_task_attendees_component ON task_attendees(component_id);

CREATE INDEX IF NOT EXISTS idx_task_components_due ON task_components(due_utc);

CREATE INDEX IF NOT EXISTS idx_task_components_object ON task_components(object_id);

CREATE INDEX IF NOT EXISTS idx_task_relations_component ON task_relations(component_id);

CREATE INDEX IF NOT EXISTS idx_task_relations_uid ON task_relations(related_uid);

CREATE INDEX IF NOT EXISTS idx_tasks_collection_version ON tasks(collection_id, version);
