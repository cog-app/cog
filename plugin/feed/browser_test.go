package feed_test

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// seedBrowserFeed gives a user a realistic reader: two folders, four
// subscriptions (one failing, one with a title too long for the sidebar),
// text, audio and video items in mixed read/starred states, and item tags.
//
// Publication times are fixed dates in a past year so the rendered dates
// ("2025-…") do not depend on the wall clock. No subscription has a favicon,
// so nothing is fetched from the network. It returns the article that the
// reader view opens.
func seedBrowserFeed(t *testing.T, env *cogtest.Env, username string) (articleID int64) {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	db, err := raw.(*feed.Plugin).DB(username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	techID, err := store.CreateFolder("Tech News")
	require.NoError(t, err)
	podID, err := store.CreateFolder("Podcasts")
	require.NoError(t, err)

	blogID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://blog.example.com/feed.xml", Title: "Example Engineering Blog",
		FolderID: sql.NullInt64{Int64: techID, Valid: true},
	})
	require.NoError(t, err)
	longID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://long.example.org/rss",
		Title: "A Subscription With An Extremely Long Title That Will Not Fit In The Sidebar",
	})
	require.NoError(t, err)
	showID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://audio.example.net/podcast.xml", Title: "Weekly Audio Show",
		FolderID: sql.NullInt64{Int64: podID, Valid: true},
	})
	require.NoError(t, err)
	chanID, err := store.CreateSubscription(feed.Subscription{
		Kind: "youtube", URL: "https://www.youtube.com/channel/UCexample", Title: "Video Channel",
	})
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE subscriptions SET last_error = ?, error_count = ? WHERE id = ?`,
		"GET https://long.example.org/rss: 503 Service Unavailable", 3, longID)
	require.NoError(t, err)

	at := func(day int) int64 { return time.Date(2025, time.June, day, 9, 30, 0, 0, time.UTC).Unix() }

	articleID, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: blogID, GUID: "a1", Type: "text", Author: "Jane Doe",
		Title:       "Designing a storage layer that survives a crash halfway through a write",
		URL:         "https://blog.example.com/posts/storage-layer",
		PublishedAt: at(20),
		ContentHTML: `<p>Most storage bugs are not in the happy path. They live in the half-second
between the write and the fsync, and they only show up when the power goes out.</p>
<h2>The write-ahead log</h2>
<p>Every change is appended to a log before it touches the main file. A recovery pass
replays the log on start-up:</p>
<ul><li>append the record</li><li>fsync the log</li><li>apply to the table</li></ul>
<blockquote><p>If it is not in the log, it did not happen.</p></blockquote>
<pre><code>func (w *WAL) Append(rec Record) error {
	if _, err := w.f.Write(rec.Bytes()); err != nil {
		return fmt.Errorf("append: %w", err)
	}
	return w.f.Sync()
}</code></pre>
<p>Further reading: https://blog.example.com/posts/a-very-long-url-that-has-no-natural-break-points-and-should-wrap-instead-of-overflowing-the-card</p>`,
	})
	require.NoError(t, err)
	require.NoError(t, store.AddTag(articleID, "storage"))
	require.NoError(t, store.AddTag(articleID, "read-later"))

	starredID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: blogID, GUID: "a2", Type: "text", Author: "Sam Lee",
		Title:       "Release notes",
		URL:         "https://blog.example.com/posts/release-notes",
		PublishedAt: at(18),
		ContentHTML: "<p>Short and sweet: three fixes and one new feature.</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkStarred(starredID, true))

	readID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: longID, GUID: "l1", Type: "text",
		Title:       "An already-read article from the subscription with the very long name",
		URL:         "https://long.example.org/read",
		PublishedAt: at(15),
		ContentHTML: "<p>This one has been read, so it should look visibly quieter than the unread rows above it.</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkRead(readID, true))

	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: showID, GUID: "p1", Type: "audio", Author: "The Hosts",
		Title:    "Episode 112: Interviews, follow-ups and a surprisingly long episode title",
		URL:      "https://audio.example.net/112",
		MediaURL: "https://audio.example.net/112.mp3", MediaMIME: "audio/mpeg",
		DurationSec: 5025, PublishedAt: at(19),
	})
	require.NoError(t, err)

	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: chanID, GUID: "v1", Type: "video",
		Title:       "Building a bookshelf in ten minutes",
		URL:         "https://www.youtube.com/watch?v=example",
		MediaURL:    "https://www.youtube.com/watch?v=example",
		DurationSec: 612, PublishedAt: at(17),
	})
	require.NoError(t, err)

	for i := range 3 {
		_, _, err = store.UpsertItem(feed.Item{
			SubscriptionID: blogID, GUID: fmt.Sprintf("f%d", i), Type: "text",
			Title:       fmt.Sprintf("Filler article %d", i+1),
			PublishedAt: at(10 - i),
			ContentHTML: "<p>A summary line long enough that it has to be truncated rather than wrapping onto two or three lines in the list, which would make every row a different height and the list hard to scan.</p>",
		})
		require.NoError(t, err)
	}
	return articleID
}

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "index", "pass")
	env.CreateUser(t, "expand", "pass")
	env.CreateUser(t, "reader", "pass")
	env.CreateUser(t, "subs", "pass")
	env.CreateUser(t, "sub", "pass")
	seedBrowserFeed(t, env, "index")
	seedBrowserFeed(t, env, "expand")
	articleID := seedBrowserFeed(t, env, "reader")
	seedBrowserFeed(t, env, "subs")
	seedBrowserFeed(t, env, "sub")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "No articles yet")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Index", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "index", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("ExpandedArticle", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "expand", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		// Click the summary line: the middle of the row is the title, a link to the reader.
		b.Click(t, ".feed-item-head .feed-item-summary-line")
		b.WaitVisible(t, ".feed-item-detail .feed-item-content")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Reader", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "reader", "pass")
		b.Navigate(t, fmt.Sprintf("/apps/feed/items/%d", articleID))
		b.WaitText(t, "The write-ahead log")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("Subscription", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "sub", "pass")
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "Designing a storage layer")
		b.ClickText(t, "A Subscription With An Extremely Long Title")
		b.WaitText(t, "Last fetch failed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SubscribeForm", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "subs", "pass")
		b.Navigate(t, "/apps/feed/settings/subscriptions")
		b.WaitVisible(t, "#add-subscription")
		b.Eval(t, `() => { document.querySelector(".subscription-list-collapse").open = true }`)
		b.WaitVisible(t, "#subscription-list")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
