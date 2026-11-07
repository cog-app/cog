-- IF COLUMN users.owner_id NOT EXIST
-- An agent is an account a user owns: it signs in only with app passwords
-- the owner issues, reaches only what is shared with it, and is the
-- identity of what it changes. Its owner's deletion takes it along.
ALTER TABLE users ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
