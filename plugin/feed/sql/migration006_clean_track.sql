-- The profanity-cleaned audio is packaged like the original: clean_track_path
-- is the clean file as an HLS audio track (pipeline/package.go), '' until the
-- packager has made it. clean_track_error says why it could not.
-- IF COLUMN downloads.clean_track_path NOT EXIST
ALTER TABLE downloads ADD COLUMN clean_track_path TEXT NOT NULL DEFAULT '';
ALTER TABLE downloads ADD COLUMN clean_track_error TEXT NOT NULL DEFAULT '';
