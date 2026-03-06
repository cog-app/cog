-- IF COLUMN cards.photo_path NOT EXIST
ALTER TABLE cards ADD COLUMN photo_path TEXT NOT NULL DEFAULT '';
