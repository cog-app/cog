package feed_test

import (
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
