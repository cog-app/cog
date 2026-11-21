CREATE TABLE IF NOT EXISTS media (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL,
    file_path TEXT NOT NULL,
    file_etag TEXT NOT NULL,
    mime_type TEXT NOT NULL,
    width INTEGER NOT NULL DEFAULT 0,
    height INTEGER NOT NULL DEFAULT 0,
    orientation INTEGER NOT NULL DEFAULT 1,
    date_taken INTEGER,
    date_indexed INTEGER NOT NULL,
    latitude REAL,
    longitude REAL,
    altitude REAL,
    camera_make TEXT NOT NULL DEFAULT '',
    camera_model TEXT NOT NULL DEFAULT '',
    lens_model TEXT NOT NULL DEFAULT '',
    focal_length REAL,
    f_number REAL,
    exposure_time TEXT NOT NULL DEFAULT '',
    iso INTEGER,
    flash INTEGER,
    duration_ms INTEGER,
    video_codec TEXT NOT NULL DEFAULT '',
    audio_codec TEXT NOT NULL DEFAULT '',
    place_name TEXT NOT NULL DEFAULT '',
    country TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    city TEXT NOT NULL DEFAULT '',
    recognition_state INTEGER NOT NULL DEFAULT 0,
    recognition_at INTEGER,
    recognition_error TEXT NOT NULL DEFAULT '',
    ocr_text TEXT NOT NULL DEFAULT '',
    fingerprint TEXT NOT NULL DEFAULT '',
    quality_score REAL NOT NULL DEFAULT -1,
    content_key TEXT NOT NULL DEFAULT '',
    scan_error TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_media_file_id ON media(file_id);
CREATE INDEX IF NOT EXISTS idx_media_file_path ON media(file_path);
CREATE INDEX IF NOT EXISTS idx_media_date_taken ON media(date_taken DESC);
CREATE INDEX IF NOT EXISTS idx_media_place ON media(country, city);
CREATE INDEX IF NOT EXISTS idx_media_camera ON media(camera_make, camera_model);
CREATE INDEX IF NOT EXISTS idx_media_recognition ON media(recognition_state);

CREATE TABLE IF NOT EXISTS albums (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    album_type INTEGER NOT NULL DEFAULT 0,
    auto_key TEXT NOT NULL DEFAULT '',
    cover_media_id INTEGER REFERENCES media(id) ON DELETE SET NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Only automatic albums are keyed: every manual album (album_type 0) has an
-- empty auto_key, so a full unique index would allow just one of them.
CREATE UNIQUE INDEX IF NOT EXISTS idx_albums_auto_key ON albums(album_type, auto_key) WHERE album_type != 0;
CREATE INDEX IF NOT EXISTS idx_albums_type ON albums(album_type);

CREATE TABLE IF NOT EXISTS album_media (
    album_id INTEGER NOT NULL REFERENCES albums(id) ON DELETE CASCADE,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    added_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (album_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_album_media_media ON album_media(media_id);

-- A chosen cover that leaves the album is forgotten; the album falls back to
-- its first photo (albumCoverCol in store.go).
CREATE TRIGGER IF NOT EXISTS album_cover_removed AFTER DELETE ON album_media
BEGIN
    UPDATE albums SET cover_media_id = NULL
    WHERE id = OLD.album_id AND cover_media_id = OLD.media_id;
END;

CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    tag_type INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS media_tags (
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    confidence REAL NOT NULL DEFAULT 1.0,
    PRIMARY KEY (media_id, tag_id)
);

CREATE TABLE IF NOT EXISTS people (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL DEFAULT '',
    representative_face_id INTEGER,
    visible INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    contact_uid TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS faces (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    person_id INTEGER REFERENCES people(id) ON DELETE SET NULL,
    x REAL NOT NULL,
    y REAL NOT NULL,
    w REAL NOT NULL,
    h REAL NOT NULL,
    embedding BLOB,
    confidence REAL NOT NULL DEFAULT 0.0,
    estimated_age REAL NOT NULL DEFAULT -1,
    quality REAL NOT NULL DEFAULT -1 -- see faceQuality; -1 for faces stored before it
);

CREATE INDEX IF NOT EXISTS idx_faces_media ON faces(media_id);
CREATE INDEX IF NOT EXISTS idx_faces_person ON faces(person_id);

CREATE TABLE IF NOT EXISTS gallery_settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rename_log (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    batch_id INTEGER NOT NULL,
    file_id INTEGER NOT NULL,
    old_path TEXT NOT NULL,
    new_path TEXT NOT NULL,
    renamed_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE INDEX IF NOT EXISTS idx_rename_log_batch ON rename_log(batch_id);

CREATE TABLE IF NOT EXISTS duplicate_groups (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS duplicate_members (
    group_id INTEGER NOT NULL REFERENCES duplicate_groups(id) ON DELETE CASCADE,
    media_id INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    is_keep INTEGER NOT NULL DEFAULT 0,
    distance REAL NOT NULL DEFAULT 0.0,
    PRIMARY KEY (group_id, media_id)
);

CREATE INDEX IF NOT EXISTS idx_duplicate_members_media ON duplicate_members(media_id);

-- Decisions about duplicates outlive the groups, which each scan rebuilds:
-- a pair someone said is not a duplicate, and the copy they chose to keep.
CREATE TABLE IF NOT EXISTS duplicate_exclusions (
    media_a INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    media_b INTEGER NOT NULL REFERENCES media(id) ON DELETE CASCADE,
    PRIMARY KEY (media_a, media_b),
    CHECK (media_a < media_b)
);

CREATE TABLE IF NOT EXISTS duplicate_keeps (
    media_id INTEGER PRIMARY KEY REFERENCES media(id) ON DELETE CASCADE
);
