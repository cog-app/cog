-- When the file on disk was last modified, as of the last time its row was
-- brought in step with it. reconcile compares it to notice a change made
-- outside the plugin that kept the size; mtime is when the plugin saw the
-- change, which is what clients are shown. 0 is not yet known.
ALTER TABLE files ADD COLUMN disk_mtime INTEGER NOT NULL DEFAULT 0;
