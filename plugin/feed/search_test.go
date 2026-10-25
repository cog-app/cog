package feed_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSearchSourceRegistered(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin registered")
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x/feed", Title: "Daily"})
	require.NoError(t, err)
	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "text", Title: "UNIQUEFINDME", ContentHTML: "<p>body</p>", URL: "https://x/item",
	})
	require.NoError(t, err)

	// The background drainer may open the user index before SearchUser does,
	// in which case SearchUser skips its full sync and the item arrives via
	// the change queue a moment later. Poll rather than assume either path.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(u.User.Username, "UNIQUEFINDME", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected feed item in search results")
}
