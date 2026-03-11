-- IF COLUMN media.recognition_error NOT EXIST
ALTER TABLE media ADD COLUMN recognition_error TEXT NOT NULL DEFAULT '';
