-- IF COLUMN mounts.kind NOT EXIST
-- A mount is of a volume, of the user's area of a storage volume, or of a
-- folder shared with them, which names no volume: volume_id becomes
-- nullable, so the table is made again.
CREATE TABLE mounts_new (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES users(id),
	user_path TEXT NOT NULL,
	kind TEXT NOT NULL DEFAULT 'volume' CHECK(kind IN ('volume', 'area', 'share')),
	volume_id TEXT REFERENCES volumes(id),
	volume_subpath TEXT NOT NULL DEFAULT '',
	share_owner_id TEXT,
	share_resource_id TEXT,
	read_only INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL,
	UNIQUE(user_id, user_path),
	CHECK((kind = 'share') = (volume_id IS NULL)),
	CHECK((kind = 'share') = (share_owner_id IS NOT NULL AND share_resource_id IS NOT NULL))
);
INSERT INTO mounts_new (id, user_id, user_path, volume_id, volume_subpath, read_only, created_at)
	SELECT id, user_id, user_path, volume_id, volume_subpath, read_only, created_at FROM mounts;
DROP TABLE mounts;
ALTER TABLE mounts_new RENAME TO mounts;
CREATE INDEX IF NOT EXISTS idx_mounts_user ON mounts(user_id);
CREATE INDEX IF NOT EXISTS idx_mounts_volume ON mounts(volume_id);
