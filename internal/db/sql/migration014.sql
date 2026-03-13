CREATE TABLE group_members_new (
	group_id TEXT NOT NULL REFERENCES groups(id),
	member_id TEXT NOT NULL,
	member_type TEXT NOT NULL CHECK(member_type IN ('USER', 'OWNER', 'GROUP')),
	PRIMARY KEY (group_id, member_id)
);
INSERT INTO group_members_new SELECT * FROM group_members;
DROP TABLE group_members;
ALTER TABLE group_members_new RENAME TO group_members;
CREATE INDEX IF NOT EXISTS idx_group_members_member ON group_members(member_id);
