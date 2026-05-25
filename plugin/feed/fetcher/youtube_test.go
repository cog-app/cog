package fetcher_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/fetcher"
)

func TestYouTubeFetch(t *testing.T) {
	abs, err := filepath.Abs("testdata")
	require.NoError(t, err)
	t.Setenv("PATH", abs+":"+os.Getenv("PATH"))

	out, err := fetcher.Fetch(context.Background(), feed.Subscription{
		Kind: "youtube", URL: "https://youtube.com/@example",
	})
	require.NoError(t, err)
	if len(out.Items) == 0 {
		t.Fatal("no items")
	}
	for _, it := range out.Items {
		if it.Type != "video" {
			t.Errorf("want video, got %s", it.Type)
		}
		if it.MediaURL == "" {
			t.Errorf("missing media_url")
		}
		if it.GUID == "" {
			t.Errorf("missing guid")
		}
	}
	if out.SubscriptionUpdates["title"] == "" {
		t.Errorf("expected channel title in SubscriptionUpdates")
	}
}
