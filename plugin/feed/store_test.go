package feed_test

import (
	"fmt"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func newStore(t *testing.T) *feed.Store {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	return feed.NewStore(db)
}

func TestFoldersCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateFolder("Development")
	require.NoError(t, err)
	if id == 0 {
		t.Fatal("zero id")
	}
	fs, err := s.ListFolders()
	require.NoError(t, err)
	if len(fs) != 1 || fs[0].Name != "Development" {
		t.Fatalf("got %+v", fs)
	}
	require.NoError(t, s.RenameFolder(id, "Dev"))
	require.NoError(t, s.DeleteFolder(id))
	fs, _ = s.ListFolders()
	if len(fs) != 0 {
		t.Fatalf("folders not deleted: %+v", fs)
	}
}

func TestSubscriptionsCRUD(t *testing.T) {
	s := newStore(t)
	id, err := s.CreateSubscription(feed.Subscription{
		Kind:            "rss",
		URL:             "https://example.com/feed.xml",
		Title:           "Example",
		PollIntervalSec: 1800,
		NextCheckAt:     time.Now().Unix(),
	})
	require.NoError(t, err)
	sub, err := s.GetSubscription(id)
	require.NoError(t, err)
	if sub.URL != "https://example.com/feed.xml" {
		t.Fatalf("got %+v", sub)
	}
	require.NoError(t, s.PauseSubscription(id, true))

	subs, err := s.ListDueSubscriptions(time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	if len(subs) != 0 {
		t.Fatal("paused subscriptions should not be due")
	}

	require.NoError(t, s.PauseSubscription(id, false))
	subs, err = s.ListDueSubscriptions(time.Now().Add(time.Hour).Unix())
	require.NoError(t, err)
	if len(subs) != 1 {
		t.Fatalf("want 1 due, got %d", len(subs))
	}
}

func TestItemsAndTags(t *testing.T) {
	s := newStore(t)
	subID, err := s.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://x/feed.xml", PollIntervalSec: 1800,
	})
	require.NoError(t, err)

	id, inserted, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "guid-1",
		Type:           "text",
		Title:          "Hello",
		ContentHTML:    "<p>body</p>",
		PublishedAt:    1000,
	})
	require.NoError(t, err)
	if !inserted || id == 0 {
		t.Fatalf("expected insert, got id=%d inserted=%v", id, inserted)
	}

	id2, inserted2, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "guid-1", Type: "text", Title: "dup",
	})
	require.NoError(t, err)
	if inserted2 {
		t.Fatal("expected dedup")
	}
	if id2 != id {
		t.Fatalf("dedup returned wrong id: %d vs %d", id2, id)
	}

	require.NoError(t, s.AddTag(id, "sunday"))
	require.NoError(t, s.AddTag(id, "weekly"))
	tags, err := s.ItemTags(id)
	require.NoError(t, err)
	if len(tags) != 2 {
		t.Fatalf("got tags %v", tags)
	}

	// AddTag is idempotent on (item, tag).
	require.NoError(t, s.AddTag(id, "sunday"))
	tags, _ = s.ItemTags(id)
	if len(tags) != 2 {
		t.Fatalf("AddTag not idempotent: %v", tags)
	}

	require.NoError(t, s.RemoveTag(id, "sunday"))
	tags, _ = s.ItemTags(id)
	if len(tags) != 1 || tags[0] != "weekly" {
		t.Fatalf("after remove: %v", tags)
	}

	require.NoError(t, s.MarkRead(id, true))
	it, err := s.GetItem(id)
	require.NoError(t, err)
	if !it.IsRead {
		t.Fatal("expected read")
	}

	require.NoError(t, s.MarkStarred(id, true))
	it, _ = s.GetItem(id)
	if !it.IsStarred {
		t.Fatal("expected starred")
	}

	require.NoError(t, s.DeleteItem(id))
	if _, err := s.GetItem(id); err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestListItemsBySubscription(t *testing.T) {
	s := newStore(t)
	subID, _ := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://y/feed.xml"})
	for i := 0; i < 3; i++ {
		_, _, err := s.UpsertItem(feed.Item{
			SubscriptionID: subID,
			GUID:           fmt.Sprintf("g%d", i),
			Type:           "text",
			Title:          fmt.Sprintf("t%d", i),
			PublishedAt:    int64(1000 + i),
		})
		require.NoError(t, err)
	}
	items, err := s.ListItemsBySubscription(subID, 10)
	require.NoError(t, err)
	if len(items) != 3 {
		t.Fatalf("got %d items", len(items))
	}
}
