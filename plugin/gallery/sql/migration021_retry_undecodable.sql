-- ONCE
-- HEIF and AVIF photos, and JPEGs cut short or past the old 50-megapixel
-- cap, failed to decode; they now decode, so they are analysed again, and
-- those that could not be measured are measured again.
UPDATE media SET width = 0, height = 0, orientation = 0
	WHERE shared_from = '' AND width = 1 AND height = 1 AND recognition_state = 2
		AND recognition_error LIKE 'image: unknown format%';
UPDATE media SET quality_score = -1, fingerprint = '', ocr_text = '', scan_error = '',
		recognition_state = 0, recognition_at = NULL, recognition_error = ''
	WHERE shared_from = '' AND recognition_state = 2 AND (
		recognition_error LIKE 'image: unknown format%'
		OR recognition_error LIKE 'image is too large to decode%'
		OR recognition_error LIKE 'invalid JPEG format%');
