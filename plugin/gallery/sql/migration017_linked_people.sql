-- IF COLUMN people.linked_from NOT EXIST
ALTER TABLE people ADD COLUMN linked_from TEXT NOT NULL DEFAULT '';
ALTER TABLE people ADD COLUMN linked_id INTEGER;
