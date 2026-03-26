-- IF COLUMN people.contact_uid NOT EXIST
ALTER TABLE people ADD COLUMN contact_uid TEXT NOT NULL DEFAULT '';
