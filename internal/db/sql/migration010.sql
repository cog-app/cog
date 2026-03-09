-- IF COLUMN users.avatar_updated_at NOT EXIST
ALTER TABLE users ADD COLUMN avatar_updated_at INTEGER NOT NULL DEFAULT 0;
