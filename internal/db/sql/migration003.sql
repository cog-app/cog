-- IF COLUMN volumes.owner_id NOT EXIST
ALTER TABLE volumes ADD COLUMN owner_id TEXT REFERENCES users(id);
