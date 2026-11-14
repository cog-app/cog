-- A title the user gives a subscription, which the poller leaves alone.
-- IF COLUMN subscriptions.title_locked NOT EXIST
ALTER TABLE subscriptions ADD COLUMN title_locked INTEGER NOT NULL DEFAULT 0;
