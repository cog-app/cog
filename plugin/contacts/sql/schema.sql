CREATE TABLE IF NOT EXISTS cards (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	fn TEXT NOT NULL DEFAULT '',
	email TEXT NOT NULL DEFAULT '',
	tel TEXT NOT NULL DEFAULT '',
	vcard_data TEXT NOT NULL,
	rev TEXT NOT NULL DEFAULT '',
	org TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	emails_json TEXT NOT NULL DEFAULT '[]',
	phones_json TEXT NOT NULL DEFAULT '[]',
	addresses_json TEXT NOT NULL DEFAULT '[]',
	photo_path TEXT NOT NULL DEFAULT '',
	is_starred INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
	UNIQUE(collection_id, uri),
	UNIQUE(collection_id, uid)
);

CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE INDEX IF NOT EXISTS idx_cards_collection_version ON cards(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_cards_uid ON cards(uid);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);
