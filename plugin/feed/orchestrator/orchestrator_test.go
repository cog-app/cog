package orchestrator_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/orchestrator"
)

func TestOrchestratorRunsPoller(t *testing.T) {
	plug := orchestrator.NewFeedPlugin().WithTickInterval(50 * time.Millisecond)
	env := cogtest.New(t, plug)
	u := env.CreateUser(t, "u", "pass")

	// Open the feed DB for this user (runs migrations, creates the DB file on disk).
	db, err := plug.DB(u.User.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	// Serve a simple RSS feed.
	abs, err := filepath.Abs(filepath.Join("..", "fetcher", "testdata", "sample-text.xml"))
	require.NoError(t, err)
	body, err := os.ReadFile(abs)
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	_, err = store.CreateSubscription(feed.Subscription{
		Kind:            "rss",
		URL:             srv.URL,
		PollIntervalSec: 60,
		NextCheckAt:     1, // already due
	})
	require.NoError(t, err)

	// cogtest does not call Start; do it manually.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	require.NoError(t, plug.Start(ctx))

	// Wait up to 2s for items to appear.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		subs, _ := store.ListSubscriptions()
		if len(subs) > 0 {
			items, _ := store.ListItemsBySubscription(subs[0].ID, 100)
			if len(items) > 0 {
				return // success
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("orchestrator did not ingest items within 2s")
}
