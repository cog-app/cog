-- App password tokens became "<id>.<secret>", stored as a SHA-256 hash of the
-- secret. Rows from before that hold a bcrypt hash ("$2...") of a token with
-- no id, which can never validate again, so they are removed.
DELETE FROM app_passwords WHERE token_hash LIKE '$2%';
