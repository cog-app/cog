-- The index was part of migration003, which a database created from
-- schema.sql skips because the fingerprint column already exists. It cannot
-- go in schema.sql: that runs before migrations, when a database older than
-- migration003 has no fingerprint column to index.
CREATE INDEX IF NOT EXISTS idx_media_fingerprint ON media(fingerprint);
