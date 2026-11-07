-- IF COLUMN trash.mount NOT EXIST
-- The mount a trashed item was trashed within: it lies in that mount's
-- trash directory (vfs.TrashDir). Items trashed before were in the root's.
ALTER TABLE trash ADD COLUMN mount TEXT NOT NULL DEFAULT '/';
