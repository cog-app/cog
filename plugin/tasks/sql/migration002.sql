-- IF COLUMN tasks.created_version NOT EXIST
ALTER TABLE tasks ADD COLUMN created_version INTEGER NOT NULL DEFAULT 0;
