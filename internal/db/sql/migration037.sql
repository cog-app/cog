-- IF COLUMN mounts.share_resource_type NOT EXIST
ALTER TABLE mounts ADD COLUMN share_resource_type TEXT NOT NULL DEFAULT 'files:directory';
