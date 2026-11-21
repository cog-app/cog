-- IF COLUMN media.content_key NOT EXIST
ALTER TABLE media ADD COLUMN content_key TEXT NOT NULL DEFAULT '';
