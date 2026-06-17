package feed_test

import (
	"strconv"
	"testing"
	"time"

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

func TestPublishedRangeSearchableViaGlobalIndex(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)

	oldT := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	newT := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()
	idOld, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "old", Type: "text", Title: "OLDONE", PublishedAt: oldT})
	require.NoError(t, err)
	idNew, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "new", Type: "text", Title: "NEWONE", PublishedAt: newT})
	require.NoError(t, err)

	require.NoError(t, env.Providers.Search.FullSyncUser(u.User.Username))

	res, err := env.Providers.Search.SearchUser(u.User.Username, ":published>2026-01-01", 50, 0)
	require.NoError(t, err)
	newID := "feed:" + strconv.FormatInt(idNew, 10)
	oldID := "feed:" + strconv.FormatInt(idOld, 10)
	var hasNew, hasOld bool
	for _, h := range res.Hits {
		if h.ID == newID {
			hasNew = true
		}
		if h.ID == oldID {
			hasOld = true
		}
	}
	if !hasNew {
		t.Fatalf("published>2026 should include NEWONE; hits=%+v", res.Hits)
	}
	if hasOld {
		t.Fatalf("published>2026 should exclude OLDONE; hits=%+v", res.Hits)
	}
}

func TestDurationRangeSearchableViaGlobalIndex(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)
	idShort, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "short", Type: "audio", Title: "SHORTONE", DurationSec: 60})
	require.NoError(t, err)
	idLong, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "long", Type: "audio", Title: "LONGONE", DurationSec: 3600})
	require.NoError(t, err)
	require.NoError(t, env.Providers.Search.FullSyncUser(u.User.Username))

	res, err := env.Providers.Search.SearchUser(u.User.Username, ":duration>600", 50, 0)
	require.NoError(t, err)
	longID := "feed:" + strconv.FormatInt(idLong, 10)
	shortID := "feed:" + strconv.FormatInt(idShort, 10)
	var hasLong, hasShort bool
	for _, h := range res.Hits {
		if h.ID == longID {
			hasLong = true
		}
		if h.ID == shortID {
			hasShort = true
		}
	}
	if !hasLong {
		t.Fatalf("duration>600 should include LONGONE; hits=%+v", res.Hits)
	}
	if hasShort {
		t.Fatalf("duration>600 should exclude SHORTONE; hits=%+v", res.Hits)
	}
}
