package feed_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// seedAudioItem creates a subscription + audio item and returns the item ID.
func seedAudioItem(t *testing.T, store *feed.Store, mediaURL string) int64 {
	t.Helper()
	subID, err := store.CreateSubscription(feed.Subscription{
		Kind:  "rss",
		URL:   "https://podcast.example.com/feed",
		Title: "Test Podcast",
	})
	require.NoError(t, err)
	itemID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "ep1",
		Type:           "audio",
		Title:          "Episode 1",
		MediaURL:       mediaURL,
	})
	require.NoError(t, err)
	return itemID
}

// feedStore returns a store for the given username using the feed plugin from env.
func feedStore(t *testing.T, env *cogtest.Env, username string) *feed.Store {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	fp := raw.(*feed.Plugin)
	db, err := fp.DB(username)
	require.NoError(t, err)
	return feed.NewStore(db)
}

// TestFeedPlayerLoadsForAudioItem verifies the detail page for an audio item
// contains an <audio> element with src pointing to the media endpoint.
func TestFeedPlayerLoadsForAudioItem(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")

	resp := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(itemID, 10), u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()

	wantSrc := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/media"
	if !strings.Contains(body, "<audio") {
		t.Errorf("detail page missing <audio> element; body len=%d", len(body))
	}
	if !strings.Contains(body, wantSrc) {
		t.Errorf("detail page missing audio src %q; body len=%d", wantSrc, len(body))
	}
	if !strings.Contains(body, "player-play") {
		t.Errorf("detail page missing play button; body len=%d", len(body))
	}
	if !strings.Contains(body, "player-time") {
		t.Errorf("detail page missing time display; body len=%d", len(body))
	}
}

// TestFeedPlayerProgressEndpoint verifies POST /items/{id}/progress saves progress.
func TestFeedPlayerProgressEndpoint(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "bob", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep2.mp3")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	postURL := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/progress"
	resp := env.PostForm(t, postURL,
		url.Values{"csrf_token": {csrfToken}, "progress": {"42.5"}},
		sessionCookie,
	)
	resp.Status(http.StatusNoContent)

	pb, err := store.GetPlayback(itemID)
	require.NoError(t, err)
	if pb.ProgressSec != 42.5 {
		t.Errorf("ProgressSec = %v, want 42.5", pb.ProgressSec)
	}
	if pb.Completed {
		t.Errorf("Completed should be false after progress update")
	}
}

// TestFeedPlayerCompleteEndpoint verifies POST /items/{id}/complete marks done + read.
func TestFeedPlayerCompleteEndpoint(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep3.mp3")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	postURL := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/complete"
	resp := env.PostForm(t, postURL,
		url.Values{"csrf_token": {csrfToken}},
		sessionCookie,
	)
	resp.Status(http.StatusOK)

	pb, err := store.GetPlayback(itemID)
	require.NoError(t, err)
	if !pb.Completed {
		t.Errorf("Completed should be true after complete endpoint")
	}

	it, err := store.GetItem(itemID)
	require.NoError(t, err)
	if !it.IsRead {
		t.Errorf("IsRead should be true after complete endpoint")
	}
}

// TestFeedRangesReturnsEmptyWithoutProfanity verifies that without the profanity
// plugin and without a local file, the unified ranges endpoint returns {"ranges":[]}.
func TestFeedRangesReturnsEmptyWithoutProfanity(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "dave", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep4.mp3")

	resp := env.Get(t, "/apps/feed/api/ranges/"+strconv.FormatInt(itemID, 10), u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()
	if !strings.Contains(body, `"ranges"`) {
		t.Errorf("ranges response missing 'ranges' key; body=%q", body)
	}
	// No profanity plugin + no local file → no ranges.
	if strings.Contains(body, `"action"`) {
		t.Errorf("expected empty ranges without profanity/local file; body=%q", body)
	}
}
