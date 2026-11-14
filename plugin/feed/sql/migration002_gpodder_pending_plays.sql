-- ONCE
-- AntennaPod uploads its listening history in the same sync that subscribes
-- to its podcasts, before Cog has fetched them, so the plays matched no
-- episode and were only stored. And when an episode had been fetched, a play
-- naming it by the feed's GUID matched the entry's show notes rather than the
-- episode: an episode's item GUID is the entry's with the enclosure's hash
-- appended (fetcher.enclosureGUID), so that one entry can carry several.

-- gpodder_episodes names each episode the way a podcast app does: by its
-- feed's URL, the entry's GUID and the enclosure URL.
CREATE VIEW IF NOT EXISTS gpodder_episodes AS
SELECT i.id AS item_id, i.media_url, i.duration_sec, s.url AS podcast,
       CASE WHEN i.guid GLOB '*#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'
            THEN substr(i.guid, 1, length(i.guid) - 13) ELSE i.guid END AS guid
FROM items i JOIN subscriptions s ON s.id = i.subscription_id
WHERE s.kind = 'rss' AND i.type IN ('audio', 'video') AND i.media_url != '';

-- gpodder_episode_plays pairs each episode with the stored plays naming it,
-- by enclosure URL or by feed and GUID, as playback would record them.
-- AntennaPod reports a finished episode as position == total; Cog keeps a
-- finished episode's progress at 0, as its player does.
CREATE VIEW IF NOT EXISTS gpodder_episode_plays AS
SELECT e.item_id, a.timestamp, a.total,
       a.total > 0 AND a.position >= a.total AS completed,
       CASE WHEN a.total > 0 AND a.position >= a.total THEN 0 ELSE a.position END AS progress
FROM gpodder_episodes e
JOIN gpodder_episode_actions a ON a.action = 'play'
     AND (a.episode = e.media_url OR (e.guid != '' AND a.guid = e.guid AND a.podcast = e.podcast));

CREATE INDEX IF NOT EXISTS idx_gpodder_episode_actions_guid ON gpodder_episode_actions(guid);

-- Plays derived from playback carry the feed's GUID.
DROP TRIGGER IF EXISTS gpodder_playback_inserted;
CREATE TRIGGER gpodder_playback_inserted AFTER INSERT ON playback
BEGIN
    DELETE FROM gpodder_episode_actions
    WHERE action = 'play' AND episode = (SELECT media_url FROM items WHERE id = NEW.item_id);
    INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total)
    SELECT e.media_url, 'play', e.podcast, e.guid, NEW.last_played_at, 0,
           CASE WHEN NEW.completed AND e.duration_sec > 0 THEN e.duration_sec ELSE CAST(NEW.progress_sec AS INTEGER) END,
           MAX(e.duration_sec, CAST(NEW.progress_sec AS INTEGER))
    FROM gpodder_episodes e WHERE e.item_id = NEW.item_id;
END;

DROP TRIGGER IF EXISTS gpodder_playback_updated;
CREATE TRIGGER gpodder_playback_updated AFTER UPDATE ON playback
BEGIN
    DELETE FROM gpodder_episode_actions
    WHERE action = 'play' AND episode = (SELECT media_url FROM items WHERE id = NEW.item_id);
    INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total)
    SELECT e.media_url, 'play', e.podcast, e.guid, NEW.last_played_at, 0,
           CASE WHEN NEW.completed AND e.duration_sec > 0 THEN e.duration_sec ELSE CAST(NEW.progress_sec AS INTEGER) END,
           MAX(e.duration_sec, CAST(NEW.progress_sec AS INTEGER))
    FROM gpodder_episodes e WHERE e.item_id = NEW.item_id;
END;

-- A newly fetched episode takes up the latest play stored for it. The
-- client's duration fills in a feed that gives none, so that the play this
-- writes back still reads as finished. Inserting playback replaces the stored
-- play with one derived from it; the two agree.
DROP TRIGGER IF EXISTS gpodder_item_inserted;
CREATE TRIGGER gpodder_item_inserted AFTER INSERT ON items
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

-- Repair what is already stored: GUIDs of derived plays, then plays that
-- matched nothing when they arrived.
UPDATE gpodder_episode_actions SET guid = e.guid
FROM gpodder_episodes e
WHERE gpodder_episode_actions.action = 'play' AND gpodder_episode_actions.episode = e.media_url
  AND gpodder_episode_actions.guid != e.guid
  AND gpodder_episode_actions.guid = (SELECT guid FROM items WHERE id = e.item_id);

UPDATE items SET duration_sec = (SELECT MAX(total) FROM gpodder_episode_plays WHERE item_id = items.id)
WHERE duration_sec = 0 AND id IN (SELECT item_id FROM gpodder_episode_plays WHERE total > 0);

CREATE TEMP TABLE gpodder_latest_plays AS
SELECT item_id, progress, completed, timestamp FROM (
    SELECT p.*, row_number() OVER (PARTITION BY item_id ORDER BY timestamp DESC) AS n
    FROM gpodder_episode_plays p
) WHERE n = 1;

INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
SELECT item_id, progress, completed, timestamp FROM gpodder_latest_plays WHERE true
ON CONFLICT (item_id) DO UPDATE SET
    progress_sec = excluded.progress_sec,
    completed = excluded.completed,
    last_played_at = excluded.last_played_at
WHERE excluded.last_played_at > playback.last_played_at;

UPDATE items SET is_read = 1, fetched_at = unixepoch()
WHERE is_read = 0 AND id IN (
    SELECT l.item_id FROM gpodder_latest_plays l JOIN playback pb ON pb.item_id = l.item_id
    WHERE l.completed AND pb.completed AND pb.last_played_at = l.timestamp);

DROP TABLE temp.gpodder_latest_plays;
