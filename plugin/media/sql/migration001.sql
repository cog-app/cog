-- IF COLUMN episodes.captions_path NOT EXIST
ALTER TABLE episodes ADD COLUMN captions_path TEXT NOT NULL DEFAULT '';
