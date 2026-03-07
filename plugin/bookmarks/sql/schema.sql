CREATE TABLE IF NOT EXISTS bookmark_folders (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    folder_id   INTEGER NOT NULL DEFAULT -1,
    sort_order  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bookmark_id, folder_id)
);

CREATE TABLE IF NOT EXISTS bookmarks (
    id           INTEGER PRIMARY KEY,
    url          TEXT NOT NULL,
    title        TEXT NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    text_content TEXT NOT NULL DEFAULT '',
    favicon_url  TEXT NOT NULL DEFAULT '',
    click_count  INTEGER NOT NULL DEFAULT 0,
    available    INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at   INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS folders (
    id         INTEGER PRIMARY KEY,
    parent_id  INTEGER REFERENCES folders(id) ON DELETE CASCADE,
    title      TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS tags (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag         TEXT NOT NULL,
    PRIMARY KEY (bookmark_id, tag)
);

CREATE INDEX IF NOT EXISTS idx_bookmark_folders_folder ON bookmark_folders(folder_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_bookmarks_url ON bookmarks(url);

CREATE INDEX IF NOT EXISTS idx_tags_tag ON tags(tag);
