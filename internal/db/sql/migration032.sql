-- IF COLUMN login_flows.ip NOT EXIST
-- The grant page shows where a login flow was started, so a user can tell
-- their own client from a link someone sent them.
ALTER TABLE login_flows ADD COLUMN ip TEXT NOT NULL DEFAULT '';
