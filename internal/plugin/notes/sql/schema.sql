CREATE TABLE IF NOT EXISTS notes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    filename TEXT NOT NULL UNIQUE,
    category TEXT NOT NULL DEFAULT '',
    favorite INTEGER NOT NULL DEFAULT 0,
    etag TEXT NOT NULL DEFAULT '',
    modified INTEGER NOT NULL DEFAULT (unixepoch()),
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_notes_category ON notes(category);

CREATE INDEX IF NOT EXISTS idx_notes_favorite ON notes(favorite);

CREATE INDEX IF NOT EXISTS idx_notes_modified ON notes(modified DESC);
