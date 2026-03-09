-- IF COLUMN volumes.owner_type NOT EXIST
ALTER TABLE volumes ADD COLUMN owner_type TEXT NOT NULL DEFAULT 'user';
