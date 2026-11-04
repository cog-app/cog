-- IF COLUMN sessions.last_seen_at NOT EXIST
-- Sessions slide: each use pushes expires_at out again, and Settings lists
-- them with the device and address they were started from.
ALTER TABLE sessions ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN ip TEXT NOT NULL DEFAULT '';
