-- IF COLUMN events.created_version NOT EXIST
ALTER TABLE events ADD COLUMN created_version INTEGER NOT NULL DEFAULT 0;
