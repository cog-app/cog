-- IF COLUMN media.scan_error NOT EXIST
ALTER TABLE media ADD COLUMN scan_error TEXT NOT NULL DEFAULT '';
