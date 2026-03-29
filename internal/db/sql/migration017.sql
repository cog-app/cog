CREATE TABLE IF NOT EXISTS federated_shares (
	id TEXT PRIMARY KEY,
	direction TEXT NOT NULL CHECK(direction IN ('outbound', 'inbound')),
	state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending', 'accepted', 'rejected', 'removed')),
	share_id TEXT REFERENCES shares(id) ON DELETE SET NULL,
	remote_id TEXT NOT NULL DEFAULT '',
	remote_host TEXT NOT NULL,
	remote_user TEXT NOT NULL,
	local_user_id TEXT NOT NULL REFERENCES users(id),
	token TEXT NOT NULL,
	resource_name TEXT NOT NULL DEFAULT '',
	resource_type TEXT NOT NULL DEFAULT 'file',
	owner_name TEXT NOT NULL DEFAULT '',
	owner_host TEXT NOT NULL DEFAULT '',
	permissions INTEGER NOT NULL DEFAULT 1,
	mount_path TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_fedshares_local_user ON federated_shares(local_user_id);

CREATE INDEX IF NOT EXISTS idx_fedshares_token ON federated_shares(token);

CREATE INDEX IF NOT EXISTS idx_fedshares_direction_state ON federated_shares(direction, state);

CREATE TABLE IF NOT EXISTS federated_share_nc_ids (
	nc_id INTEGER PRIMARY KEY AUTOINCREMENT,
	federated_share_id TEXT NOT NULL UNIQUE REFERENCES federated_shares(id) ON DELETE CASCADE
);
