-- ONCE
-- UpsertAutoAlbum once read LastInsertId after an upsert that updated,
-- which returns the connection's previous insert rather than the album, so
-- media landed in the wrong auto-albums. Each scan regenerates auto-album
-- membership, so clearing it lets the next scan rebuild it correctly. Once
-- only: run on every open it would empty auto-albums before each scan.
DELETE FROM album_media WHERE album_id IN (SELECT id FROM albums WHERE album_type != 0);
