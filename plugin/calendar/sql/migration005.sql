-- IF COLUMN calendars.timezone NOT EXIST
ALTER TABLE calendars ADD COLUMN timezone TEXT NOT NULL DEFAULT '';
