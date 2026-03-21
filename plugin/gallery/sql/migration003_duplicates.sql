-- IF COLUMN media.fingerprint NOT EXIST
ALTER TABLE media ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_media_fingerprint ON media(fingerprint);

CREATE TABLE IF NOT EXISTS duplicate_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS duplicate_members (
    group_id INTEGER NOT NULL REFERENCES duplicate_groups(id) ON DELETE CASCADE,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    is_keep INTEGER NOT NULL DEFAULT 0,
    distance REAL NOT NULL DEFAULT 0.0,
    PRIMARY KEY (group_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_duplicate_members_media ON duplicate_members(media_id);
