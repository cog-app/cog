package feed_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestFeedSearchPublishedRange(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)

	// old item (2025) and new item (2026)
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	recent := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC).Unix()
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "old", Type: "text", Title: "OLD-ARTICLE", PublishedAt: old})
	require.NoError(t, err)
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "new", Type: "text", Title: "NEW-ARTICLE", PublishedAt: recent})
	require.NoError(t, err)

	// :published>2026-01-01 -> only NEW-ARTICLE
	resp := env.Get(t, "/apps/feed/?q="+url.QueryEscape(":published>2026-01-01"), u.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	if !strings.Contains(body, ">NEW-ARTICLE<") {
		t.Errorf("range query missing NEW-ARTICLE; body len=%d", len(body))
	}
	if strings.Contains(body, ">OLD-ARTICLE<") {
		t.Errorf("range query should exclude OLD-ARTICLE")
	}
}
