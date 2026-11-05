-- ONCE
-- An email address now belongs to one account. Where several held the same
-- address (in any case), none can be told from the others as its owner, so
-- each loses it and confirms it again from Settings.
UPDATE users SET email = '' WHERE email != '' AND lower(email) IN (
	SELECT lower(email) FROM users WHERE email != '' GROUP BY lower(email) HAVING COUNT(*) > 1
);
