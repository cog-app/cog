package feed_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestSearchIndexesAfterFullSync proves the incremental path: capture the
// change in the database, drain it, index it.
//
// Every other search test writes its row before anything opens the index, so
// the full sync at open picks it up and the incremental path is never
// exercised. Here the index is opened and synced while the database is empty,
// so a hit can only come from a change captured after that point.
//
// The test harness does not install update hooks, so nothing nudges the
// drainer either — this is the same path a backend with no hooks would take.
func TestSearchIndexesAfterFullSync(t *testing.T) {
	old := search.DrainInterval
	search.DrainInterval = 100 * time.Millisecond
	t.Cleanup(func() { search.DrainInterval = old })

	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	// Open the index and sync it against an empty database.
	_, err := env.Providers.Search.SearchUser(u.User.Username, "nothing", 10, 0)
	require.NoError(t, err)

	p, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin registered")
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x/feed", Title: "Daily"})
	require.NoError(t, err)
	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "text",
		Title: "LATERINSERTION", ContentHTML: "<p>body</p>", URL: "https://x/item",
	})
	require.NoError(t, err)

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(u.User.Username, "LATERINSERTION", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	t.Fatal("item written after the index was synced never reached the index")
}
