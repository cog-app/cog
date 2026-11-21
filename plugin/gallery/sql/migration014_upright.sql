-- ONCE
-- EXIF orientation was ignored: photos stored turned were measured, tagged
-- and searched for faces on their side. Dimensions are now stored upright,
-- and what was worked out from the sideways pixels is worked out again.
UPDATE media SET width = height, height = width WHERE orientation >= 5;
UPDATE media SET recognition_state = 0, recognition_at = NULL, recognition_error = '',
    quality_score = -1, fingerprint = ''
WHERE orientation NOT IN (0, 1) AND mime_type LIKE 'image/%';
