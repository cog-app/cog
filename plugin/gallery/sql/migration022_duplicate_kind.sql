-- IF COLUMN duplicate_groups.kind NOT EXIST
ALTER TABLE duplicate_groups ADD COLUMN kind INTEGER NOT NULL DEFAULT 0;
