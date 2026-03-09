-- Migrate legacy GROUP member_type to USER (GROUP type replaced by OWNER/USER).
UPDATE group_members SET member_type = 'USER' WHERE member_type = 'GROUP';

-- Remove group mounts (no longer used).
DROP TABLE IF EXISTS group_mounts;

-- Migrate group-owned volumes to unowned.
UPDATE volumes SET owner_id = NULL WHERE owner_type = 'group';

-- Migrate legacy share resource type.
UPDATE shares SET resource_type = 'files:volume' WHERE resource_type = 'system:volume';
