-- IF COLUMN dav_collections.kind NOT EXIST
ALTER TABLE dav_collections ADD COLUMN kind TEXT NOT NULL DEFAULT '';
