CREATE TABLE IF NOT EXISTS downloads (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK(state IN ('queued','downloading','ready','error')),
    local_path TEXT NOT NULL DEFAULT '',
    clean_audio_path TEXT NOT NULL DEFAULT '',
    captions_path TEXT NOT NULL DEFAULT '',
    bytes INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL DEFAULT 0,
    completed_at INTEGER NOT NULL DEFAULT 0,
    needs_profanity INTEGER NOT NULL DEFAULT 0,
    needs_transcribe INTEGER NOT NULL DEFAULT 0,
    silence_ranges TEXT NOT NULL DEFAULT '',
    silence_state TEXT NOT NULL DEFAULT '',
    video_path TEXT NOT NULL DEFAULT '',
    package_state TEXT NOT NULL DEFAULT '',
    package_error TEXT NOT NULL DEFAULT '',
    clean_track_path TEXT NOT NULL DEFAULT '',
    clean_track_error TEXT NOT NULL DEFAULT '',
    edges_wanted INTEGER NOT NULL DEFAULT 0,
    edges_edit TEXT NOT NULL DEFAULT '',
    edges_error TEXT NOT NULL DEFAULT '',
    edges_index TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS feed_settings (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS filters (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    sort_order INTEGER NOT NULL DEFAULT 0,
    query TEXT NOT NULL DEFAULT '*',
    action TEXT NOT NULL CHECK(action IN ('skip','tag','download','mute_profanity','transcribe')),
    action_value TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS gpodder_episode_actions (
    episode TEXT NOT NULL,
    action TEXT NOT NULL CHECK(action IN ('new','download','play','delete')),
    podcast TEXT NOT NULL,
    guid TEXT NOT NULL DEFAULT '',
    timestamp INTEGER NOT NULL,
    started INTEGER NOT NULL DEFAULT -1,
    position INTEGER NOT NULL DEFAULT -1,
    total INTEGER NOT NULL DEFAULT -1,
    changed_at INTEGER NOT NULL DEFAULT (unixepoch()),
    PRIMARY KEY (episode, action)
);

CREATE TABLE IF NOT EXISTS gpodder_removed_subscriptions (
    url TEXT PRIMARY KEY,
    removed_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS hls_edits (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    id TEXT NOT NULL,
    cuts TEXT NOT NULL,
    duration REAL NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (item_id, id)
);

CREATE TABLE IF NOT EXISTS item_tags (
    item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
    PRIMARY KEY (item_id, tag_id)
);

CREATE TABLE IF NOT EXISTS items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subscription_id INTEGER NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    guid TEXT NOT NULL,
    type TEXT NOT NULL CHECK(type IN ('text','audio','video')),
    title TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    url TEXT NOT NULL DEFAULT '',
    content_html TEXT NOT NULL DEFAULT '',
    media_url TEXT NOT NULL DEFAULT '',
    media_mime TEXT NOT NULL DEFAULT '',
    duration_sec INTEGER NOT NULL DEFAULT 0,
    thumbnail_url TEXT NOT NULL DEFAULT '',
    published_at INTEGER NOT NULL DEFAULT 0,
    fetched_at INTEGER NOT NULL DEFAULT (unixepoch()),
    is_read INTEGER NOT NULL DEFAULT 0,
    is_starred INTEGER NOT NULL DEFAULT 0,
    content_hash TEXT NOT NULL DEFAULT '',
    UNIQUE(subscription_id, guid)
);

CREATE TABLE IF NOT EXISTS playback (
    item_id INTEGER PRIMARY KEY REFERENCES items(id) ON DELETE CASCADE,
    progress_sec REAL NOT NULL DEFAULT 0,
    completed INTEGER NOT NULL DEFAULT 0,
    last_played_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS playlists (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    query TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    play_order TEXT NOT NULL DEFAULT 'newest' CHECK(play_order IN ('newest','oldest','shortest','shuffled')),
    shuffle_seed INTEGER NOT NULL DEFAULT 0,
    listen_only INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    folder_id INTEGER REFERENCES folders(id) ON DELETE SET NULL,
    kind TEXT NOT NULL CHECK(kind IN ('rss','youtube','url')),
    url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    site_url TEXT NOT NULL DEFAULT '',
    image_url TEXT NOT NULL DEFAULT '',
    favicon_url TEXT NOT NULL DEFAULT '',
    etag TEXT NOT NULL DEFAULT '',
    last_modified TEXT NOT NULL DEFAULT '',
    poll_interval_sec INTEGER NOT NULL DEFAULT 1800,
    next_check_at INTEGER NOT NULL DEFAULT 0,
    last_checked_at INTEGER NOT NULL DEFAULT 0,
    last_success_at INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    paused INTEGER NOT NULL DEFAULT 0,
    title_locked INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE
);

CREATE INDEX IF NOT EXISTS idx_downloads_state ON downloads(state);

CREATE INDEX IF NOT EXISTS idx_filters_order ON filters(sort_order);

CREATE INDEX IF NOT EXISTS idx_gpodder_episode_actions_changed ON gpodder_episode_actions(changed_at);

CREATE INDEX IF NOT EXISTS idx_gpodder_episode_actions_guid ON gpodder_episode_actions(guid);

CREATE INDEX IF NOT EXISTS idx_items_media_url ON items(media_url);

CREATE INDEX IF NOT EXISTS idx_items_published ON items(published_at DESC);

CREATE INDEX IF NOT EXISTS idx_items_starred ON items(is_starred, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_items_subscription ON items(subscription_id);

CREATE INDEX IF NOT EXISTS idx_items_type ON items(type, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_items_unread ON items(is_read, published_at DESC);

CREATE INDEX IF NOT EXISTS idx_playback_last ON playback(last_played_at DESC);

CREATE INDEX IF NOT EXISTS idx_subscriptions_folder ON subscriptions(folder_id);

CREATE INDEX IF NOT EXISTS idx_subscriptions_next_check ON subscriptions(next_check_at);

CREATE TRIGGER IF NOT EXISTS episode_marked_read AFTER UPDATE OF is_read ON items
WHEN NEW.is_read AND NOT OLD.is_read AND OLD.type IN ('audio', 'video') AND NEW.media_url != ''
BEGIN
    INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
    VALUES (NEW.id, 0, 1, unixepoch())
    ON CONFLICT (item_id) DO UPDATE SET progress_sec = 0, completed = 1, last_played_at = unixepoch()
    WHERE NOT playback.completed;
END;

CREATE TRIGGER IF NOT EXISTS episode_marked_unread AFTER UPDATE OF is_read ON items
WHEN NOT NEW.is_read AND OLD.is_read AND OLD.type IN ('audio', 'video') AND NEW.media_url != ''
BEGIN
    DELETE FROM playback WHERE item_id = NEW.id;
    DELETE FROM gpodder_episode_actions WHERE action = 'play' AND episode = NEW.media_url;
    INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp)
    SELECT e.media_url, 'new', e.podcast, e.guid, unixepoch()
    FROM gpodder_episodes e WHERE e.item_id = NEW.id
    ON CONFLICT (episode, action) DO UPDATE SET
        podcast = excluded.podcast, guid = excluded.guid,
        timestamp = excluded.timestamp, changed_at = unixepoch();
END;

CREATE VIEW IF NOT EXISTS gpodder_episode_plays AS
SELECT e.item_id, a.timestamp, a.total,
       a.total > 0 AND a.position >= a.total AS completed,
       CASE WHEN a.total > 0 AND a.position >= a.total THEN 0 ELSE a.position END AS progress
FROM gpodder_episodes e
JOIN gpodder_episode_actions a ON a.action = 'play'
     AND (a.episode = e.media_url OR (e.guid != '' AND a.guid = e.guid AND a.podcast = e.podcast));

CREATE VIEW IF NOT EXISTS gpodder_episodes AS
SELECT i.id AS item_id, i.media_url, i.duration_sec, s.url AS podcast,
       CASE WHEN i.guid GLOB '*#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'
            THEN substr(i.guid, 1, length(i.guid) - 13) ELSE i.guid END AS guid
FROM items i JOIN subscriptions s ON s.id = i.subscription_id
WHERE s.kind = 'rss' AND i.type IN ('audio', 'video') AND i.media_url != '';

CREATE TRIGGER IF NOT EXISTS gpodder_item_inserted AFTER INSERT ON items
WHEN NEW.type IN ('audio', 'video') AND NEW.media_url != ''
BEGIN
    UPDATE items SET duration_sec = (SELECT MAX(total) FROM gpodder_episode_plays WHERE item_id = NEW.id)
    WHERE id = NEW.id AND duration_sec = 0
      AND EXISTS (SELECT 1 FROM gpodder_episode_plays WHERE item_id = NEW.id AND total > 0);
    INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
    SELECT item_id, progress, completed, timestamp FROM gpodder_episode_plays
    WHERE item_id = NEW.id ORDER BY timestamp DESC LIMIT 1;
    UPDATE items SET is_read = 1 WHERE id = NEW.id
      AND EXISTS (SELECT 1 FROM playback WHERE item_id = NEW.id AND completed);
END;

CREATE TRIGGER IF NOT EXISTS gpodder_playback_inserted AFTER INSERT ON playback
BEGIN
    DELETE FROM gpodder_episode_actions
    WHERE action = 'play' AND episode = (SELECT media_url FROM items WHERE id = NEW.item_id);
    INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total)
    SELECT e.media_url, 'play', e.podcast, e.guid, NEW.last_played_at, 0,
           CASE WHEN NEW.completed AND e.duration_sec > 0 THEN e.duration_sec ELSE CAST(NEW.progress_sec AS INTEGER) END,
           MAX(e.duration_sec, CAST(NEW.progress_sec AS INTEGER))
    FROM gpodder_episodes e WHERE e.item_id = NEW.item_id;
END;

CREATE TRIGGER IF NOT EXISTS gpodder_playback_updated AFTER UPDATE ON playback
BEGIN
    DELETE FROM gpodder_episode_actions
    WHERE action = 'play' AND episode = (SELECT media_url FROM items WHERE id = NEW.item_id);
    INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total)
    SELECT e.media_url, 'play', e.podcast, e.guid, NEW.last_played_at, 0,
           CASE WHEN NEW.completed AND e.duration_sec > 0 THEN e.duration_sec ELSE CAST(NEW.progress_sec AS INTEGER) END,
           MAX(e.duration_sec, CAST(NEW.progress_sec AS INTEGER))
    FROM gpodder_episodes e WHERE e.item_id = NEW.item_id;
END;

CREATE TRIGGER IF NOT EXISTS gpodder_subscription_added AFTER INSERT ON subscriptions
BEGIN
    DELETE FROM gpodder_removed_subscriptions WHERE url = NEW.url;
END;

CREATE TRIGGER IF NOT EXISTS gpodder_subscription_removed AFTER DELETE ON subscriptions
WHEN OLD.kind = 'rss'
BEGIN
    DELETE FROM gpodder_removed_subscriptions WHERE url = OLD.url;
    INSERT INTO gpodder_removed_subscriptions (url) VALUES (OLD.url);
END;
