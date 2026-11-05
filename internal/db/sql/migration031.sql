-- IF COLUMN group_invitations.invitee_id NOT EXIST
-- An invitation may name an existing user, who must accept it: owners no
-- longer add people to groups without their consent.
ALTER TABLE group_invitations ADD COLUMN invitee_id TEXT NOT NULL DEFAULT '';
