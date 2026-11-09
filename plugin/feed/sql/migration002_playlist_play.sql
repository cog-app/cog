-- How a playlist plays, the same on every device: its order (a shuffle is a
-- seed, so it holds still until reshuffled) and whether its videos play as
-- audio.
-- IF COLUMN playlists.play_order NOT EXIST
ALTER TABLE playlists ADD COLUMN play_order TEXT NOT NULL DEFAULT 'newest' CHECK(play_order IN ('newest','oldest','shortest','shuffled'));
ALTER TABLE playlists ADD COLUMN shuffle_seed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE playlists ADD COLUMN listen_only INTEGER NOT NULL DEFAULT 0;
