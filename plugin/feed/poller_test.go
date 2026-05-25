package feed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

// realFetch is a FetchFunc that delegates to the real fetcher package.
// Defined here to break the import cycle: feed cannot import feed/fetcher,
// but the test binary (package feed_test) can import both.
func realFetch(ctx context.Context, sub feed.Subscription) (feed.FetchResult, error) {
	return fetcher.Fetch(ctx, sub)
}

func newPollerEnv(t *testing.T) (*feed.Store, *feed.Poller) {
	t.Helper()
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := p.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	return s, feed.NewPoller(s).WithFetcher(realFetch)
}

// helper: serve a simple RSS feed from fixtures
func newRSSServer(t *testing.T) *httptest.Server {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("fetcher", "testdata", "sample-text.xml"))
	require.NoError(t, err)
	body, err := os.ReadFile(abs)
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
}

func TestPollerRunOncePullsItems(t *testing.T) {
	store, p := newPollerEnv(t)
	srv := newRSSServer(t)
	defer srv.Close()

	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, err)

	require.NoError(t, p.RunOnce(context.Background()))

	items, err := store.ListItemsBySubscription(subID, 100)
	require.NoError(t, err)
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}

	// Subscription next_check_at should be bumped past now, and last_success_at set.
	sub, _ := store.GetSubscription(subID)
	if sub.NextCheckAt <= 1 {
		t.Errorf("next_check_at not bumped: %d", sub.NextCheckAt)
	}
	if sub.LastSuccessAt == 0 {
		t.Errorf("last_success_at not set")
	}
}

func TestPollerErrorBackoff(t *testing.T) {
	store, p := newPollerEnv(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	subID, _ := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1,
	})
	require.NoError(t, p.RunOnce(context.Background()))

	sub, _ := store.GetSubscription(subID)
	if sub.ErrorCount != 1 {
		t.Errorf("want error_count=1, got %d", sub.ErrorCount)
	}
	if sub.LastError == "" {
		t.Errorf("want last_error populated")
	}
	if sub.NextCheckAt <= 1 {
		t.Errorf("next_check_at not bumped past 1: %d", sub.NextCheckAt)
	}
}

func TestPollerSkipsPaused(t *testing.T) {
	store, p := newPollerEnv(t)
	srv := newRSSServer(t)
	defer srv.Close()

	subID, _ := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: srv.URL, PollIntervalSec: 60, NextCheckAt: 1, Paused: true,
	})
	require.NoError(t, p.RunOnce(context.Background()))

	items, _ := store.ListItemsBySubscription(subID, 100)
	if len(items) != 0 {
		t.Fatalf("paused subscription was polled: %d items", len(items))
	}
}
