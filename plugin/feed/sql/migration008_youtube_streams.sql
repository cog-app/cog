-- The googlevideo URLs yt-dlp resolved for an undownloaded YouTube item, one
-- row per track, so a player's repeated playlist and segment requests do not
-- each run yt-dlp. format_id pins the formats across a refresh (the segment
-- index is made against them); expires_at is when the URL stops working
-- (unix seconds).
CREATE TABLE IF NOT EXISTS youtube_streams (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('audio','video')),
    format_id TEXT NOT NULL,
    url TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (item_id, kind)
);
