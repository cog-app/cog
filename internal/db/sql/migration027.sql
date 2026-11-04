-- IF COLUMN users.totp_last_step NOT EXIST
-- The time step of the last TOTP code accepted for the user. A code is
-- accepted only for a later step, so one seen over a shoulder or captured by
-- a phishing page cannot be used a second time.
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
