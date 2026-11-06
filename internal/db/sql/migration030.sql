-- Runs after migration029 has removed duplicates, which an existing database
-- may hold; schema.sql runs first and so cannot create it.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email_unique ON users(lower(email)) WHERE email != '';
