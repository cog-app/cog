-- ONCE
-- Playback recorded before the gpodder sync triggers existed becomes play
-- actions, so a podcast app's first sync learns what was already listened to.
INSERT OR IGNORE INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total)
SELECT i.media_url, 'play', s.url, i.guid, pb.last_played_at, 0,
       CASE WHEN pb.completed AND i.duration_sec > 0 THEN i.duration_sec ELSE CAST(pb.progress_sec AS INTEGER) END,
       MAX(i.duration_sec, CAST(pb.progress_sec AS INTEGER))
FROM playback pb
JOIN items i ON i.id = pb.item_id
JOIN subscriptions s ON s.id = i.subscription_id
WHERE i.media_url != '' AND s.kind = 'rss';
