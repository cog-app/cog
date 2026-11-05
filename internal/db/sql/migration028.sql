-- IF COLUMN sessions.hashed NOT EXIST
-- Sessions are stored by the SHA-256 of their token, so the table no longer
-- holds live credentials. Rows from before keep the token itself (hashed = 0)
-- until their next use rewrites them, so nobody is signed out by the change.
ALTER TABLE sessions ADD COLUMN hashed INTEGER NOT NULL DEFAULT 0;
