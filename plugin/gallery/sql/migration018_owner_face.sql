-- IF COLUMN faces.owner_face_id NOT EXIST
ALTER TABLE faces ADD COLUMN owner_face_id INTEGER;
