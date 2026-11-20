-- IF COLUMN volumes.kind NOT EXIST
-- Every volume made before kinds is raw: its shares go on meaning what they
-- meant until an admin makes it storage.
ALTER TABLE volumes ADD COLUMN kind TEXT NOT NULL DEFAULT 'raw' CHECK(kind IN ('raw', 'storage'));
