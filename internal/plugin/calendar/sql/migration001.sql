-- IF COLUMN calendars.sync_token NOT EXIST
ALTER TABLE calendars ADD COLUMN sync_token INTEGER NOT NULL DEFAULT 0;

CREATE TABLE IF NOT EXISTS deleted_events (
	calendar_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	deleted_at_token INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deleted_events_cal ON deleted_events(calendar_id, deleted_at_token);
