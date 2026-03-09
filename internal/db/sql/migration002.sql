-- IF COLUMN users.display_name NOT EXIST
ALTER TABLE users ADD COLUMN display_name TEXT NOT NULL DEFAULT '';
