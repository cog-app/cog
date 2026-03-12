-- NC-compatible integer IDs for shares. The NC Android app requires
-- share IDs to be integers (Integer.parseInt in ShareXMLParser).
-- This mapping table auto-assigns an integer for each internal share ID.
CREATE TABLE IF NOT EXISTS share_nc_ids (
	nc_id INTEGER PRIMARY KEY AUTOINCREMENT,
	share_id TEXT NOT NULL UNIQUE REFERENCES shares(id) ON DELETE CASCADE
);
