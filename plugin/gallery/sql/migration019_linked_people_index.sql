-- Not in schema.sql: that runs before migrations, when an older database
-- has no linked_from column to index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_people_linked ON people(linked_from, linked_id) WHERE linked_from != '';
