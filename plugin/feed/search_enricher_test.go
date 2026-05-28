package feed_test

import (
	"database/sql"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// feedDB returns the per-user feed DB the enricher would see.
// Uses Plugin.DB() to ensure schema migrations have run.
func feedDB(t *testing.T, env *cogtest.Env, username string) *sql.DB {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(username)
	require.NoError(t, err)
	return db
}

func TestFeedEnrichSchemaPopulatesFields(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	u.Seed(t)

	db := feedDB(t, env, u.Username)
	s := feed.NewStore(db)

	subID, err := s.CreateSubscription(feed.Subscription{
		Kind:  "rss",
		URL:   "https://x.test/f",
		Title: "SubT",
	})
	require.NoError(t, err)

	id, _, err := s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "g1",
		Type:           "audio",
		Title:          "Hello",
		Author:         "Alice",
		ContentHTML:    "<p>body</p>",
		URL:            "https://x.test/1",
		PublishedAt:    100,
	})
	require.NoError(t, err)

	en := feed.NewEnricher()
	docs, err := en.EnrichSchema(db, []int64{id})
	require.NoError(t, err)
	if len(docs) != 1 {
		t.Fatalf("got %d docs, want 1", len(docs))
	}
	d := docs[0]
	wantID := "feed:" + strconv.FormatInt(id, 10)
	if d.ID != wantID {
		t.Fatalf("ID = %q, want %q", d.ID, wantID)
	}
	if d.Fields["title"] != "Hello" {
		t.Fatalf("title = %v", d.Fields["title"])
	}
	if d.Fields["author"] != "Alice" {
		t.Fatalf("author = %v", d.Fields["author"])
	}
	if d.Fields["kind"] != "rss" {
		t.Fatalf("kind = %v", d.Fields["kind"])
	}
	if d.Fields["subscription"] != "SubT" {
		t.Fatalf("subscription = %v", d.Fields["subscription"])
	}
	// New items start unread.
	is, _ := d.Fields["is"].([]string)
	hasUnread := false
	for _, v := range is {
		if v == "unread" {
			hasUnread = true
		}
	}
	if !hasUnread {
		t.Fatalf("expected 'unread' in is field, got %v", is)
	}
}

func TestFeedEnrichSchemaEmpty(t *testing.T) {
	en := feed.NewEnricher()
	docs, err := en.EnrichSchema(nil, nil)
	require.NoError(t, err)
	if len(docs) != 0 {
		t.Fatalf("expected 0 docs for empty input, got %d", len(docs))
	}
}
