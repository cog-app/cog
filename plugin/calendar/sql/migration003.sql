-- IF COLUMN schedule_inbox.created_version NOT EXIST
ALTER TABLE schedule_inbox ADD COLUMN created_version INTEGER NOT NULL DEFAULT 0;
