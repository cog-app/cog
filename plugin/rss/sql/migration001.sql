-- IF COLUMN feeds.auto_read_pattern NOT EXIST
ALTER TABLE feeds ADD COLUMN auto_read_pattern TEXT NOT NULL DEFAULT '';
