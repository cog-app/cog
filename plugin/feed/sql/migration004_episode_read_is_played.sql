-- An episode is read when it is played, whichever app says so. Marking it
-- read (in NextCloud News, or the web UI) finishes it, which the playback
-- triggers tell podcast apps. Marking it unread forgets its playback and
-- tells them it is new. Plays and "new" from podcast apps set is_read
-- themselves (gpoddersync), after playback, so these find nothing to change.
--
-- OLD.type keeps out migration003, which turns show notes into an episode:
-- reading the notes before then was not listening.
--
-- A feed that gives no duration is reported finished at 0 of 0.

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
