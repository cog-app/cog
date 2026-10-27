-- IF COLUMN dav_birthday_cursors.book_owner_id NOT EXIST
ALTER TABLE dav_birthday_cursors ADD COLUMN book_owner_id TEXT NOT NULL DEFAULT '';
