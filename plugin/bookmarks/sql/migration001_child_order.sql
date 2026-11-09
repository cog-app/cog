-- IF COLUMN folders.sort_order NOT EXIST
-- A folder's place among its parent's children, which it shares with the
-- bookmarks in that parent (bookmark_folders.sort_order). Sync clients such as
-- Floccus keep the order of a folder's contents, folders and bookmarks mixed.
ALTER TABLE folders ADD COLUMN sort_order INTEGER NOT NULL DEFAULT 0;
