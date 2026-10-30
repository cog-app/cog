-- An album's chosen cover must be one of its photos. Taking that photo out
-- of the album (or deleting it, which cascades to album_media) clears the
-- choice, so the album falls back to its first photo instead of showing one
-- it no longer holds. schema.sql creates the same trigger for new databases.
CREATE TRIGGER IF NOT EXISTS album_cover_removed AFTER DELETE ON album_media
BEGIN
    UPDATE albums SET cover_media_id = NULL
    WHERE id = OLD.album_id AND cover_media_id = OLD.media_id;
END;

-- Covers chosen before the trigger existed may name a photo already taken
-- out of the album.
UPDATE albums SET cover_media_id = NULL
WHERE cover_media_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM album_media am
                  WHERE am.album_id = albums.id AND am.media_id = albums.cover_media_id);
