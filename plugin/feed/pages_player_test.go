package feed_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
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

// TestAdjacentPlayerEndpoint verifies prev/next navigation renders the neighbor
// player, returns 204 at the ends, and 400 on bad direction.
func TestAdjacentPlayerEndpoint(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "erin", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://erin.example.com/feed", Title: "Erin"})
	require.NoError(t, err)
	first, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "a", Type: "audio", Title: "A", MediaURL: "https://cdn.example.com/a.mp3", PublishedAt: 100})
	require.NoError(t, err)
	second, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "b", Type: "audio", Title: "B", MediaURL: "https://cdn.example.com/b.mp3", PublishedAt: 200})
	require.NoError(t, err)

	// The all-view lists items by published_at DESC: [second(200), first(100)].
	// From second, dir=next → first (lower index+1 in DESC list = older item).
	resp := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(second, 10)+"/adjacent?dir=next&ctx=all", u.Auth)
	resp.Status(http.StatusOK)
	body := resp.ReadBody()
	if !strings.Contains(body, "feed-player") {
		t.Errorf("next player missing #feed-player markup; body len=%d", len(body))
	}
	if !strings.Contains(body, strconv.FormatInt(first, 10)) {
		t.Errorf("next player should reference item %d", first)
	}

	// next of first → 204 (first is at the end of the DESC list, no newer-index neighbor).
	env.Get(t, "/apps/feed/items/"+strconv.FormatInt(first, 10)+"/adjacent?dir=next&ctx=all", u.Auth).
		Status(http.StatusNoContent)

	// bad direction → 400.
	env.Get(t, "/apps/feed/items/"+strconv.FormatInt(first, 10)+"/adjacent?dir=up", u.Auth).
		Status(http.StatusBadRequest)
}

// seedTwoMedia creates a subscription with two audio items and returns their IDs
// ordered as (older, newer) by published_at. In the "all" view (DESC order),
// the list is [newer, older], so from newer dir=next reaches older.
func seedTwoMedia(t *testing.T, env *cogtest.Env, username string) (older, newer int64) {
	t.Helper()
	store := feedStore(t, env, username)
	subID, err := store.CreateSubscription(feed.Subscription{
		Kind:  "rss",
		URL:   "https://ctx-test.example.com/feed",
		Title: "Ctx Test Podcast",
	})
	require.NoError(t, err)
	older, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "ctx-ep1",
		Type:           "audio",
		Title:          "Old Episode",
		MediaURL:       "https://cdn.example.com/old.mp3",
		PublishedAt:    1000,
	})
	require.NoError(t, err)
	newer, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "ctx-ep2",
		Type:           "audio",
		Title:          "New Episode",
		MediaURL:       "https://cdn.example.com/new.mp3",
		PublishedAt:    2000,
	})
	require.NoError(t, err)
	return older, newer
}

// TestAdjacentPlayerCarriesCtx verifies that the adjacent player endpoint
// respects the view context and carries ctx data attributes in the response.
func TestAdjacentPlayerCarriesCtx(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "frank", "pass")
	// all-view is DESC by published_at: [newer(2000), older(1000)]
	// From newer, dir=next → older (higher index in DESC list).
	older, newer := seedTwoMedia(t, env, u.Username)
	resp := env.Get(t, fmt.Sprintf("/apps/feed/items/%d/adjacent?dir=next&ctx=all", newer), u.Auth)
	resp.Status(200)
	resp.Contains(`data-ctx="ctx=all"`)
	resp.Contains(fmt.Sprintf(`data-item-id="%d"`, older))
}

// The ranges endpoint never analyses a file itself: until the pipeline has,
// a ready download's silence ranges are pending, and afterwards they are read
// from its row.
func TestRangesPendingUntilAnalysed(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "frank", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep5.mp3")
	require.NoError(t, store.EnqueueDownload(itemID))
	require.NoError(t, store.MarkDownloadStart(itemID))
	require.NoError(t, store.MarkDownloadReady(itemID, "media/5.mp3", 10))
	path := "/apps/feed/api/ranges/" + strconv.FormatInt(itemID, 10)

	var got struct {
		Ranges  []feed.Range `json:"ranges"`
		Pending bool         `json:"pending"`
	}
	env.Get(t, path, u.Auth).Status(http.StatusOK).JSON(&got)
	assert.True(t, got.Pending)
	assert.Equal(t, 0, len(got.Ranges))

	require.NoError(t, store.SetSilenceRanges(itemID, []feed.Range{{Start: 1, End: 2, Action: "skip"}}))
	got.Pending, got.Ranges = true, nil
	env.Get(t, path, u.Auth).Status(http.StatusOK).JSON(&got)
	assert.False(t, got.Pending)
	assert.Equal(t, []feed.Range{{Start: 1, End: 2, Action: "skip"}}, got.Ranges)
}
