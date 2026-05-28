package feed_test

import (
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestIsDownloadedSearchableViaGlobalIndex exercises Plan A end-to-end: a feed
// item with a downloads row in state='ready' shows up under is:downloaded;
// one without does not.
func TestIsDownloadedSearchableViaGlobalIndex(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok)
	fp, ok := raw.(*feed.Plugin)
	require.True(t, ok)

	db, err := fp.DB(u.User.Username)
	require.NoError(t, err)

	s := feed.NewStore(db)

	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)

	// Item A — has a completed download.
	idA, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "a", Type: "audio", Title: "ALPHA-NEEDLE", PublishedAt: 100,
	})
	require.NoError(t, err)

	// Item B — no download row.
	idB, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "b", Type: "audio", Title: "BETA-NEEDLE", PublishedAt: 200,
	})
	require.NoError(t, err)

	// Create a downloads row for A in state='ready'.
	_, err = db.Exec(`INSERT INTO downloads (item_id, state) VALUES (?, 'ready')`, idA)
	require.NoError(t, err)

	// Trigger a full sync for this user so the index contains the latest data.
	require.NoError(t, env.Providers.Search.FullSyncUser(u.User.Username))

	// Confirm A is returned for is:downloaded; B is not.
	res, err := env.Providers.Search.SearchUser(u.User.Username, "is:downloaded", 50, 0)
	require.NoError(t, err)
	if res.Total < 1 {
		t.Fatalf("expected at least 1 hit for is:downloaded, got %d", res.Total)
	}

	wantID := "feed:" + strconv.FormatInt(idA, 10)
	dontWant := "feed:" + strconv.FormatInt(idB, 10)
	var hasA, hasB bool
	for _, h := range res.Hits {
		if h.ID == wantID {
			hasA = true
		}
		if h.ID == dontWant {
			hasB = true
		}
	}
	if !hasA {
		t.Fatalf("alpha (downloaded) missing from results: %+v", res.Hits)
	}
	if hasB {
		t.Fatal("beta (no download) should not appear under is:downloaded")
	}
}
