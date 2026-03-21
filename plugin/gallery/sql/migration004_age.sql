-- IF COLUMN faces.estimated_age NOT EXIST
ALTER TABLE faces ADD COLUMN estimated_age REAL NOT NULL DEFAULT -1;
