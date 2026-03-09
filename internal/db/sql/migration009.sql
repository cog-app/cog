-- IF COLUMN group_invitations.member_type NOT EXIST
ALTER TABLE group_invitations ADD COLUMN member_type TEXT NOT NULL DEFAULT 'USER';
