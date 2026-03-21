-- IF COLUMN media.quality_score NOT EXIST
ALTER TABLE media ADD COLUMN quality_score REAL NOT NULL DEFAULT -1;
