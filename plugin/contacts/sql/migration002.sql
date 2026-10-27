-- IF COLUMN cards.created_version NOT EXIST
ALTER TABLE cards ADD COLUMN created_version INTEGER NOT NULL DEFAULT 0;
