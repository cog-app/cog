-- ONCE
-- An RSS entry with an enclosure used to become two items: a text item
-- holding its body (the show notes) under the entry's GUID, and a media item
-- per enclosure under the entry's GUID with the enclosure URL's hash
-- appended. They had separate read state, and a sync filter could not tell a
-- podcast's notes from an article. The fetcher now makes the entry's first
-- enclosure the entry itself: its GUID and body. This brings what is stored
-- into the same shape. The first enclosure is the lowest id.
--
-- An entry stored as notes plus episode becomes the notes' item (and id)
-- with the episode's media and children (downloads, playback, tags).

CREATE TEMP TABLE feed_show_notes_merge AS
SELECT t.id AS text_id, (
    SELECT MIN(m.id) FROM items m
    WHERE m.subscription_id = t.subscription_id AND m.type IN ('audio', 'video')
      AND length(m.guid) = length(t.guid) + 13
      AND substr(m.guid, 1, length(t.guid) + 1) = t.guid || '#'
) AS media_id
FROM items t
WHERE t.type = 'text';

DELETE FROM temp.feed_show_notes_merge WHERE media_id IS NULL;

-- Children move while the text item is still text, so the GPodder triggers
-- on playback find no episode for it and leave the stored plays alone.
--
-- Both halves can have been played (the reader recorded progress on the
-- notes too). They become one: finished if either was, at the position of
-- whichever was played last. The episode's download is the one with media,
-- so it replaces any the notes had.
UPDATE playback SET
    completed = MAX(playback.completed, e.completed),
    progress_sec = CASE WHEN e.last_played_at > playback.last_played_at THEN e.progress_sec ELSE playback.progress_sec END,
    last_played_at = MAX(playback.last_played_at, e.last_played_at)
FROM temp.feed_show_notes_merge m JOIN playback e ON e.item_id = m.media_id
WHERE playback.item_id = m.text_id;

DELETE FROM playback WHERE item_id IN (
    SELECT m.media_id FROM temp.feed_show_notes_merge m
    WHERE EXISTS (SELECT 1 FROM playback t WHERE t.item_id = m.text_id));

DELETE FROM downloads WHERE item_id IN (
    SELECT m.text_id FROM temp.feed_show_notes_merge m
    WHERE EXISTS (SELECT 1 FROM downloads d WHERE d.item_id = m.media_id));

UPDATE downloads SET item_id = (SELECT text_id FROM temp.feed_show_notes_merge WHERE media_id = downloads.item_id)
WHERE item_id IN (SELECT media_id FROM temp.feed_show_notes_merge);

UPDATE playback SET item_id = (SELECT text_id FROM temp.feed_show_notes_merge WHERE media_id = playback.item_id)
WHERE item_id IN (SELECT media_id FROM temp.feed_show_notes_merge);

INSERT OR IGNORE INTO item_tags (item_id, tag_id)
SELECT m.text_id, it.tag_id FROM item_tags it JOIN temp.feed_show_notes_merge m ON m.media_id = it.item_id;

CREATE TEMP TABLE feed_show_notes_media AS
SELECT m.text_id, i.type, i.media_url, i.media_mime, i.duration_sec, i.thumbnail_url,
       i.is_read, i.is_starred
FROM items i JOIN temp.feed_show_notes_merge m ON m.media_id = i.id;

DELETE FROM items WHERE id IN (SELECT media_id FROM temp.feed_show_notes_merge);

-- Read when either half was (the notes read, or the episode finished), so
-- nothing done in one app comes back unread. The text item's type was not an
-- episode's, so marking it read here does not finish it
-- (migration004_episode_read_is_played).
UPDATE items SET
    type = m.type, media_url = m.media_url, media_mime = m.media_mime,
    duration_sec = m.duration_sec, thumbnail_url = m.thumbnail_url,
    is_read = MAX(items.is_read, m.is_read), is_starred = MAX(items.is_starred, m.is_starred),
    fetched_at = unixepoch()
FROM temp.feed_show_notes_media m
WHERE items.id = m.text_id;

DROP TABLE temp.feed_show_notes_media;
DROP TABLE temp.feed_show_notes_merge;

-- An episode stored without notes is its entry's first enclosure: it takes
-- the entry's GUID, unless an item already has it.
CREATE TEMP TABLE feed_episode_guids AS
SELECT MIN(i.id) AS id, substr(i.guid, 1, length(i.guid) - 13) AS guid
FROM items i JOIN subscriptions s ON s.id = i.subscription_id
WHERE s.kind = 'rss' AND i.type IN ('audio', 'video')
  AND i.guid GLOB '*#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'
GROUP BY i.subscription_id, substr(i.guid, 1, length(i.guid) - 13);

UPDATE items SET guid = g.guid, fetched_at = unixepoch()
FROM temp.feed_episode_guids g
WHERE items.id = g.id
  AND NOT EXISTS (SELECT 1 FROM items o WHERE o.subscription_id = items.subscription_id AND o.guid = g.guid);

DROP TABLE temp.feed_episode_guids;
