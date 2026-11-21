-- available_at is when googlevideo starts serving a cached URL (unix seconds,
-- yt-dlp's available_at; 0 = no wait): asked for sooner, it answers 403.
-- IF COLUMN youtube_streams.available_at NOT EXIST
ALTER TABLE youtube_streams ADD COLUMN available_at INTEGER NOT NULL DEFAULT 0;
