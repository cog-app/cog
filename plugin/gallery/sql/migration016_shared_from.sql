-- IF COLUMN media.shared_from NOT EXIST
ALTER TABLE media ADD COLUMN shared_from TEXT NOT NULL DEFAULT '';
