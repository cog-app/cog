package feed_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestWidgetRendersUnreadAndRecent(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	p, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	plug := p.(*feed.Plugin)

	db, err := plug.DB(u.User.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "u", Title: "Daily"})
	require.NoError(t, err)

	_, _, err = store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g1", Type: "text", Title: "UnreadOne", PublishedAt: 1000})
	require.NoError(t, err)
	_, _, err = store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g2", Type: "text", Title: "UnreadTwo", PublishedAt: 2000})
	require.NoError(t, err)
	id3, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g3", Type: "text", Title: "ReadItem", PublishedAt: 3000})
	require.NoError(t, err)
	require.NoError(t, store.MarkRead(id3, true))

	var buf bytes.Buffer
	require.NoError(t, plug.RenderWidget(context.Background(), &buf, u.User.Username))

	body := buf.String()
	if !strings.Contains(body, "2") {
		t.Errorf("expected unread count 2 in widget output: %q", body)
	}
	if !strings.Contains(body, "UnreadTwo") {
		t.Errorf("expected UnreadTwo in widget output: %q", body)
	}
	if !strings.Contains(body, "UnreadOne") {
		t.Errorf("expected UnreadOne in widget output: %q", body)
	}
	if strings.Contains(body, "ReadItem") {
		t.Errorf("read item should not appear in widget output: %q", body)
	}
}
