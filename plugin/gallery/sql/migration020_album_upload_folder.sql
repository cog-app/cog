-- IF COLUMN albums.upload_folder NOT EXIST
ALTER TABLE albums ADD COLUMN upload_folder TEXT NOT NULL DEFAULT '';
