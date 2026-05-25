package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSearchSourceRegistered(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	subID, _ := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x/feed", Title: "Daily"})
	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g", Type: "text", Title: "UNIQUEFINDME", ContentHTML: "<p>body</p>", URL: "https://x/item",
	})
	require.NoError(t, err)

	// SearchUser implicitly opens the user index and performs a FullSync,
	// which picks up the item we just inserted.
	res, err := env.Providers.Search.SearchUser(u.User.Username, "UNIQUEFINDME", 10, 0)
	require.NoError(t, err)
	if res == nil || len(res.Hits) == 0 {
		t.Fatal("expected feed item in search results")
	}
}
