package feed_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestFilterEngineActions(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	itemID, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g1", Type: "audio",
		Title: "Morning Brew",
	})
	require.NoError(t, err)

	_, err = s.CreateFilter(feed.Filter{
		SortOrder: 1, Query: "type:audio", Action: "tag", ActionValue: "podcast",
	})
	require.NoError(t, err)
	_, err = s.CreateFilter(feed.Filter{
		SortOrder: 2, Query: "tag:podcast", Action: "download",
	})
	require.NoError(t, err)

	eng, err := feed.NewFilterEngine(s)
	require.NoError(t, err)

	require.NoError(t, eng.Apply(itemID))

	tags, err := s.ItemTags(itemID)
	require.NoError(t, err)
	if len(tags) != 1 || tags[0] != "podcast" {
		t.Fatalf("got tags %v", tags)
	}
	dl, err := s.GetDownload(itemID)
	require.NoError(t, err)
	if dl.State != "queued" {
		t.Fatalf("got download state %q", dl.State)
	}
}

func TestFilterSkipDeletesItemAndStops(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "text", Title: "spam ad"})

	// Skip is first; tag would run after, but skip cuts the chain.
	_, _ = s.CreateFilter(feed.Filter{SortOrder: 1, Query: "spam", Action: "skip"})
	_, _ = s.CreateFilter(feed.Filter{SortOrder: 2, Query: "type:text", Action: "tag", ActionValue: "news"})

	eng, _ := feed.NewFilterEngine(s)
	require.NoError(t, eng.Apply(id))

	if _, err := s.GetItem(id); err == nil {
		t.Fatal("expected item deleted")
	}
}

func TestFilterDownloadSkippedForTextItem(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u"})
	id, _, _ := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g", Type: "text", Title: "x"})
	_, _ = s.CreateFilter(feed.Filter{SortOrder: 1, Query: "*", Action: "download"})

	eng, _ := feed.NewFilterEngine(s)
	require.NoError(t, eng.Apply(id))

	if _, err := s.GetDownload(id); err == nil {
		t.Fatal("expected no download row for text item")
	}
}

func TestParseFilterQueryRejectsInvalid(t *testing.T) {
	if _, err := feed.ParseFilterQuery("(((unbalanced"); err == nil {
		t.Fatal("expected parse error")
	}
	if _, err := feed.ParseFilterQuery("type:audio"); err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
}
