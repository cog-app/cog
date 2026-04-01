-- IF COLUMN episodes.clean_audio_path NOT EXIST
ALTER TABLE episodes ADD COLUMN clean_audio_path TEXT NOT NULL DEFAULT '';
