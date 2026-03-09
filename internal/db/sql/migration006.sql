-- IF COLUMN groups.owner_id NOT EXIST
ALTER TABLE groups ADD COLUMN owner_id TEXT REFERENCES users(id);
