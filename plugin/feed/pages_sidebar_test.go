package feed_test

import (
	"fmt"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestSubscriptionSettingsHasSidebar(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	id, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f"})
	require.NoError(t, err)
	resp := env.Get(t, fmt.Sprintf("/apps/feed/subscription/%d/settings", id), u.Auth)
	resp.Status(200)
	resp.Contains("feed-sidebar")
}
