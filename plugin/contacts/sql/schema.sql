CREATE TABLE IF NOT EXISTS card_addresses (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	-- Each component is a list of values, comma-separated with commas and
	-- backslashes in a value escaped (internal/dav/vcard).
	pobox TEXT NOT NULL DEFAULT '',
	ext TEXT NOT NULL DEFAULT '',
	street TEXT NOT NULL DEFAULT '',
	locality TEXT NOT NULL DEFAULT '',
	region TEXT NOT NULL DEFAULT '',
	code TEXT NOT NULL DEFAULT '',
	country TEXT NOT NULL DEFAULT '',
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_emails (
	-- EMAIL. card_phones, card_urls, card_impp and card_related have the same
	-- columns. types is a JSON array; group_name is the vCard property group
	-- ("item1"), label the X-ABLabel paired with it; params holds the
	-- parameters Cog does not model (JSON).
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_impp (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_members (
	-- A group card's members: MEMBER (4.0) or X-ADDRESSBOOKSERVER-MEMBER (3.0).
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	uri TEXT NOT NULL,
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_phones (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_related (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS card_urls (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	card_id INTEGER NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	value TEXT NOT NULL,
	types TEXT NOT NULL DEFAULT '[]',
	pref INTEGER NOT NULL DEFAULT 0,
	group_name TEXT NOT NULL DEFAULT '',
	label TEXT NOT NULL DEFAULT '',
	params TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS cards (
	-- One CardDAV resource. The vCard text is generated from these rows on
	-- read (internal/dav/vcard); nothing stores it.
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	collection_id INTEGER NOT NULL,
	uid TEXT NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	-- The version that created the row: JMAP's /changes tells resources
	-- created since a state from those only updated.
	created_version INTEGER NOT NULL DEFAULT 0,
	-- Length of the text served for this version, for getcontentlength.
	size INTEGER NOT NULL DEFAULT 0,
	-- VERSION as received ('2.1', '3.0', '4.0'), written back unchanged.
	vcard_version TEXT NOT NULL DEFAULT '',
	-- KIND (4.0) or X-ADDRESSBOOKSERVER-KIND (3.0): '' for an individual.
	kind TEXT NOT NULL DEFAULT '',
	fn TEXT NOT NULL DEFAULT '',
	-- N's components as value lists, like card_addresses; NULL without N.
	n_family TEXT,
	n_given TEXT,
	n_additional TEXT,
	n_prefix TEXT,
	n_suffix TEXT,
	-- NICKNAME, ORG's units and CATEGORIES as JSON arrays; NULL when absent.
	nickname TEXT,
	-- ORG: the organisation, and its units after it.
	org TEXT NOT NULL DEFAULT '',
	org_units TEXT,
	title TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	-- BDAY, ANNIVERSARY and GENDER as written; their syntax depends on the
	-- version.
	bday TEXT NOT NULL DEFAULT '',
	anniversary TEXT NOT NULL DEFAULT '',
	gender TEXT NOT NULL DEFAULT '',
	rev TEXT NOT NULL DEFAULT '',
	categories TEXT,
	-- An inline PHOTO, stored as a file named by the SHA-256 of its bytes;
	-- '' for none. type and encoding are as written, params the parameters
	-- Cog does not model (JSON). A photo given by URL has photo_uri, and
	-- photo_hash names its cached copy ('u-' and the SHA-256 of the URL).
	photo_hash TEXT NOT NULL DEFAULT '',
	photo_uri TEXT NOT NULL DEFAULT '',
	photo_type TEXT NOT NULL DEFAULT '',
	photo_encoding TEXT NOT NULL DEFAULT '',
	photo_params TEXT NOT NULL DEFAULT '[]',
	-- Cog's own; not part of the vCard.
	is_starred INTEGER NOT NULL DEFAULT 0,
	-- Properties, components and parameters Cog does not model (JSON).
	leftovers TEXT NOT NULL DEFAULT '{}',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL,
	UNIQUE (collection_id, uri),
	UNIQUE (collection_id, uid)
);

CREATE TABLE IF NOT EXISTS dav_sync (
	collection_id INTEGER PRIMARY KEY,
	token INTEGER NOT NULL DEFAULT 0,
	floor INTEGER NOT NULL DEFAULT 0
, modified INTEGER NOT NULL DEFAULT 0);

CREATE TABLE IF NOT EXISTS dav_tombstones (
	collection_id INTEGER NOT NULL,
	uri TEXT NOT NULL,
	version INTEGER NOT NULL,
	deleted_at INTEGER NOT NULL,
	PRIMARY KEY (collection_id, uri)
);

CREATE INDEX IF NOT EXISTS idx_card_addresses_card ON card_addresses(card_id);

CREATE INDEX IF NOT EXISTS idx_card_emails_card ON card_emails(card_id, position);

CREATE INDEX IF NOT EXISTS idx_card_emails_value ON card_emails(value COLLATE NOCASE);

CREATE INDEX IF NOT EXISTS idx_card_impp_card ON card_impp(card_id);

CREATE INDEX IF NOT EXISTS idx_card_members_card ON card_members(card_id);

CREATE INDEX IF NOT EXISTS idx_card_phones_card ON card_phones(card_id, position);

CREATE INDEX IF NOT EXISTS idx_card_related_card ON card_related(card_id);

CREATE INDEX IF NOT EXISTS idx_card_urls_card ON card_urls(card_id);

CREATE INDEX IF NOT EXISTS idx_cards_collection_version ON cards(collection_id, version);

CREATE INDEX IF NOT EXISTS idx_cards_photo ON cards(photo_hash) WHERE photo_hash != '';

CREATE INDEX IF NOT EXISTS idx_cards_uid ON cards(uid);

CREATE INDEX IF NOT EXISTS idx_dav_tombstones_version ON dav_tombstones(collection_id, version);
