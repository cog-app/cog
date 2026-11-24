-- IF COLUMN duplicate_members.score NOT EXIST
ALTER TABLE duplicate_members ADD COLUMN score REAL NOT NULL DEFAULT -1;
