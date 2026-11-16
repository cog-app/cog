-- Skipping silence plays a presentation with the silences cut out. The cut
-- ends are short re-encoded "edge" files (pipeline/edges.go). edges_wanted is
-- set when a player asks for the skip master; edges_edit is the edit list the
-- edges were made for ('' = none yet), edges_error says why they could not be.
-- edges_index records where each edge's init and media are in its file (JSON,
-- feed.EdgesIndex), so playlists list edges without opening them.
-- hls_edits keeps the cuts of every edit list a player was served, so a
-- progress report can map its position back onto the item. duration is the
-- media duration the cuts were planned against, where the item ends.
-- IF COLUMN downloads.edges_wanted NOT EXIST
ALTER TABLE downloads ADD COLUMN edges_wanted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE downloads ADD COLUMN edges_edit TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN edges_error TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN edges_index TEXT NOT NULL DEFAULT '';
CREATE TABLE IF NOT EXISTS hls_edits (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    cuts TEXT NOT NULL,
    duration REAL NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (item_id, id)
);
