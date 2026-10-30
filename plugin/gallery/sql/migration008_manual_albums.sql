-- idx_albums_type_key made (album_type, auto_key) unique for every album, and
-- every manual album has album_type 0 and an empty auto_key, so a user could
-- create only one. schema.sql now creates idx_albums_auto_key, which covers
-- automatic albums only; drop the old index it replaces.
DROP INDEX IF EXISTS idx_albums_type_key;
