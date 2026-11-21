-- ONCE
-- Sharpness was measured on the 256 px thumbnail and is now measured on a
-- 1024 px copy; scores on the two scales do not compare.
UPDATE media SET quality_score = -1 WHERE quality_score != -1;
