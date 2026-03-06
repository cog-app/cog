CREATE TABLE IF NOT EXISTS address_books (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	uid TEXT NOT NULL UNIQUE,
	name TEXT NOT NULL DEFAULT 'Contacts',
	description TEXT NOT NULL DEFAULT '',
	color TEXT NOT NULL DEFAULT '#0082c9',
	sort_order INTEGER NOT NULL DEFAULT 0,
	sync_token INTEGER NOT NULL DEFAULT 0,
	ctag INTEGER NOT NULL DEFAULT 1,
	created_at INTEGER NOT NULL DEFAULT (unixepoch()),
	updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS cards (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	address_book_id INTEGER NOT NULL REFERENCES address_books(id) ON DELETE CASCADE,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	etag TEXT NOT NULL,
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
	UNIQUE(address_book_id, uid)
);

CREATE TABLE IF NOT EXISTS deleted_cards (
	address_book_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	deleted_at_token INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_cards_book ON cards(address_book_id);

CREATE INDEX IF NOT EXISTS idx_cards_uid ON cards(uid);

CREATE INDEX IF NOT EXISTS idx_cards_uri ON cards(uri);

CREATE INDEX IF NOT EXISTS idx_deleted_cards_book ON deleted_cards(address_book_id, deleted_at_token);
