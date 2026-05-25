package feed_test

import (
	"context"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// TestPollerFilterEngineRunsOnInsert verifies that the FilterEngine is applied
// to each newly-inserted item when wired via WithFilterEngine.
func TestPollerFilterEngineRunsOnInsert(t *testing.T) {
	store, poller := newPollerEnv(t)
	srv := newRSSServer(t)
	defer srv.Close()

	// Filter: tag every item with "auto".
	_, err := store.CreateFilter(feed.Filter{
		SortOrder: 1, Query: "*", Action: "tag", ActionValue: "auto",
	})
	require.NoError(t, err)

	eng, err := feed.NewFilterEngine(store)
	require.NoError(t, err)
	poller.WithFilterEngine(eng)

	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, err)

	require.NoError(t, poller.RunOnce(context.Background()))

	items, err := store.ListItemsBySubscription(subID, 100)
	require.NoError(t, err)
	if len(items) == 0 {
		t.Fatal("expected items after poll")
	}

	for _, it := range items {
		tags, err := store.ItemTags(it.ID)
		require.NoError(t, err)
		found := false
		for _, tag := range tags {
			if tag == "auto" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("item %d (%q): expected tag %q, got %v", it.ID, it.Title, "auto", tags)
		}
	}
}

// TestPollerFilterEngineSkipRemovesItem verifies that a "skip" filter action
// causes the item to be deleted immediately after insertion.
func TestPollerFilterEngineSkipRemovesItem(t *testing.T) {
	store, poller := newPollerEnv(t)
	srv := newRSSServer(t)
	defer srv.Close()

	// Skip every item.
	_, err := store.CreateFilter(feed.Filter{
		SortOrder: 1, Query: "*", Action: "skip",
	})
	require.NoError(t, err)

	eng, err := feed.NewFilterEngine(store)
	require.NoError(t, err)
	poller.WithFilterEngine(eng)

	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, err)

	require.NoError(t, poller.RunOnce(context.Background()))

	items, err := store.ListItemsBySubscription(subID, 100)
	require.NoError(t, err)
	if len(items) != 0 {
		t.Fatalf("expected 0 items after skip filter, got %d", len(items))
	}
}

// TestPollerWithoutFilterEngineStillWorks ensures the poller functions
// correctly when no filter engine is attached (WithFilterEngine not called).
func TestPollerWithoutFilterEngineStillWorks(t *testing.T) {
	store, poller := newPollerEnv(t)
	srv := newRSSServer(t)
	defer srv.Close()

	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, err)

	// No WithFilterEngine call — should not panic or error.
	require.NoError(t, poller.RunOnce(context.Background()))

	items, err := store.ListItemsBySubscription(subID, 100)
	require.NoError(t, err)
	if len(items) == 0 {
		t.Fatal("expected items without filter engine")
	}
}
