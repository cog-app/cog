package feed_test

import (
	"os"
	"strconv"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Episodes stored as show notes plus enclosure are merged into one item: the
// notes' id and the entry's GUID, the episode's media and children, read or
// starred if either half was. An episode without notes takes its entry's
// GUID; articles and a second enclosure are left alone.
func TestMergeShowNotesMigration(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	var sub int64
	require.NoError(t, db.QueryRow(`INSERT INTO subscriptions (kind, url) VALUES ('rss', 'https://pod.example/feed.xml') RETURNING id`).Scan(&sub))
	item := func(guid, typ, mediaURL, content string, read, starred bool) int64 {
		t.Helper()
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: guid, Type: typ, Title: guid,
			MediaURL: mediaURL, ContentHTML: content, IsRead: read, IsStarred: starred, DurationSec: 600})
		require.NoError(t, err)
		return id
	}
	notes := item("ep1", "text", "", "<p>notes</p>", true, false)
	ep := item("ep1#0123456789ab", "audio", "https://pod.example/ep1.mp3", "", false, true)
	second := item("ep1#ba9876543210", "video", "https://pod.example/ep1.mp4", "", false, false)
	bare := item("ep2#0123456789ab", "audio", "https://pod.example/ep2.mp3", "", false, false)
	post := item("post", "text", "", "<p>post</p>", false, false)
	require.NoError(t, store.SetPlayback(ep, 30, false, time.Now()))
	require.NoError(t, store.AddTag(ep, "later"))
	require.NoError(t, store.AddTag(notes, "news"))

	// Both halves played and downloaded: the reader recorded progress on the
	// notes too. This is what failed with "UNIQUE constraint failed:
	// playback.item_id" in a real database.
	notes3 := item("ep3", "text", "", "<p>notes 3</p>", false, false)
	ep3 := item("ep3#0123456789ab", "audio", "https://pod.example/ep3.mp3", "", false, false)
	_, err = db.Exec(`INSERT INTO playback (item_id, progress_sec, completed, last_played_at) VALUES (?1, 100, 0, 1000), (?2, 50, 1, 2000)`, notes3, ep3)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO downloads (item_id, state, local_path) VALUES (?1, 'error', ''), (?2, 'ready', 'ep3.mp3')`, notes3, ep3)
	require.NoError(t, err)

	_, err = db.Exec(`DELETE FROM _cog_migrations WHERE name = 'sql/migration003_merge_show_notes.sql'`)
	require.NoError(t, err)
	require.NoError(t, plugin.Migrate(db, os.DirFS(".")))

	_, err = store.GetItem(ep)
	require.Error(t, err)
	merged, err := store.GetItem(notes)
	require.NoError(t, err)
	require.Equal(t, "ep1", merged.GUID)
	require.Equal(t, "audio", merged.Type)
	require.Equal(t, "https://pod.example/ep1.mp3", merged.MediaURL)
	require.Equal(t, "<p>notes</p>", merged.ContentHTML)
	require.Equal(t, int64(600), merged.DurationSec)
	require.True(t, merged.IsRead)
	require.True(t, merged.IsStarred)
	tags, err := store.ItemTags(notes)
	require.NoError(t, err)
	require.Equal(t, []string{"later", "news"}, tags)
	var progress float64
	var completed bool
	require.NoError(t, db.QueryRow(`SELECT progress_sec, completed FROM playback WHERE item_id = ?`, notes).Scan(&progress, &completed))
	require.Equal(t, 30.0, progress)
	// Reading the notes before the merge was not listening.
	require.False(t, completed)

	// Finished if either half was, at the position of the half played last;
	// the episode's download, which has the media.
	require.NoError(t, db.QueryRow(`SELECT progress_sec, completed FROM playback WHERE item_id = ?`, notes3).Scan(&progress, &completed))
	require.Equal(t, 50.0, progress)
	require.True(t, completed)
	var state, path string
	require.NoError(t, db.QueryRow(`SELECT state, local_path FROM downloads WHERE item_id = ?`, notes3).Scan(&state, &path))
	require.Equal(t, "ready", state)
	require.Equal(t, "ep3.mp3", path)
	var left int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM playback WHERE item_id = ?`, ep3).Scan(&left))
	require.Equal(t, 0, left)

	for id, guid := range map[int64]string{second: "ep1#ba9876543210", bare: "ep2", post: "post"} {
		it, err := store.GetItem(id)
		require.NoError(t, err)
		require.Equal(t, guid, it.GUID)
	}
}

// The reader shows an episode's player with its show notes beneath, and
// opening it is not listening: it stays unread.
func TestReaderShowsEpisodeNotes(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example/feed.xml", Title: "Pod"})
	require.NoError(t, err)
	ep, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "ep1", Type: "audio", Title: "Ep 1",
		MediaURL: "https://pod.example/ep1.mp3", MediaMIME: "audio/mpeg", ContentHTML: "<p>Show notes here.</p>"})
	require.NoError(t, err)

	env.Get(t, "/apps/feed/items/"+strconv.FormatInt(ep, 10), u.Auth).Status(200).
		Contains("feed-player").Contains(`aria-label="Show notes"`).Contains("Show notes here.")
	it, err := store.GetItem(ep)
	require.NoError(t, err)
	require.False(t, it.IsRead)
}
