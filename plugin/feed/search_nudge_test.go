package feed_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestSearchIndexesFromNudge covers the path a self-hosted server actually
// takes: the update hook fires on the write and tells the drainer to run,
// rather than the change waiting for the periodic sweep.
//
// The sweep is pushed out of reach so it cannot be the reason a document
// appears. If the hook stops nudging, this fails; the sweep-only path is
// covered separately by TestSearchIndexesAfterFullSync.
func TestSearchIndexesFromNudge(t *testing.T) {
	old := search.DrainInterval
	search.DrainInterval = 10 * time.Minute
	t.Cleanup(func() { search.DrainInterval = old })

	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	// Open the index and sync it while the database is empty, so a later hit
	// cannot have come from the full sync.
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
		Title: "NUDGEDINTOINDEX", ContentHTML: "<p>body</p>", URL: "https://x/item",
	})
	require.NoError(t, err)

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchUser(u.User.Username, "NUDGEDINTOINDEX", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}

	t.Fatal("write did not reach the index without waiting for the periodic sweep")
}
