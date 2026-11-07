-- IF COLUMN activities.notify NOT EXIST
-- A notification is activity addressed to the user, such as something shared
-- with them: the tray counts it and may show it on the desktop. The rest of
-- the log is only listed.
ALTER TABLE activities ADD COLUMN notify INTEGER NOT NULL DEFAULT 0;

-- The tray reads a user's latest entries, all of them, only notifications,
-- or one app's.
CREATE INDEX IF NOT EXISTS idx_activities_user_notify ON activities(user_id, notify, id);
CREATE INDEX IF NOT EXISTS idx_activities_user_app ON activities(user_id, app, id);
