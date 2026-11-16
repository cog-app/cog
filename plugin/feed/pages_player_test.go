package feed_test

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
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

// The web player's YouTube video downloads first, as ResolveMedia's does:
// stream-url queues it and says so, then answers the media route once the
// download is ready.
func TestStreamURLVideoDownloadsFirst(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "erin", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://www.youtube.com/channel/UC1", Title: "Talks"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "v1", Type: "video", Title: "Talk", MediaURL: "https://www.youtube.com/watch?v=abc"})
	require.NoError(t, err)
	path := "/apps/feed/api/stream-url/" + strconv.FormatInt(id, 10)

	var waiting struct {
		URL         string `json:"url"`
		Downloading bool   `json:"downloading"`
		Message     string `json:"message"`
	}
	env.Get(t, path, u.Auth).Status(http.StatusOK).JSON(&waiting)
	if !waiting.Downloading || waiting.URL != "" || waiting.Message == "" {
		t.Errorf("before the download: %+v, want downloading with a message", waiting)
	}
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	if d.State != "queued" {
		t.Errorf("download state %q, want queued", d.State)
	}

	require.NoError(t, store.MarkDownloadStart(id))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp4", 10))
	var ready struct {
		URL         string `json:"url"`
		Downloading bool   `json:"downloading"`
	}
	env.Get(t, path, u.Auth).Status(http.StatusOK).JSON(&ready)
	if want := "/apps/feed/items/" + strconv.FormatInt(id, 10) + "/media"; ready.URL != want || ready.Downloading {
		t.Errorf("after the download: %+v, want url %q", ready, want)
	}
}

// twoTrackDownload is a YouTube video downloaded as two tracks, not packaged yet.
func twoTrackDownload(t *testing.T, store *feed.Store) int64 {
	t.Helper()
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://www.youtube.com/channel/UC9", Title: "Talks"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "v9", Type: "video", Title: "Talk", MediaURL: "https://www.youtube.com/watch?v=v9"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkVideoDownloadReady(id, "media/1.m4a", "media/1.video.mp4", 2))
	return id
}

func streamURL(t *testing.T, env *cogtest.Env, u *cogtest.User, id int64) map[string]any {
	t.Helper()
	var got map[string]any
	env.Get(t, "/apps/feed/api/stream-url/"+strconv.FormatInt(id, 10), u.Auth).Status(http.StatusOK).JSON(&got)
	return got
}

func TestStreamURLPackaged(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	got := streamURL(t, env, u, id)
	hlsURL, _ := got["hls"].(string)
	assert.True(t, strings.HasPrefix(hlsURL, "/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/master.m3u8?sig="), got)
}

func TestStreamURLPreparing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	id := twoTrackDownload(t, feedStore(t, env, "alice"))
	got := streamURL(t, env, u, id)
	assert.Equal(t, true, got["downloading"])
	assert.Equal(t, "Preparing the video", got["message"])
}

func TestStreamURLPackageError(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	id := twoTrackDownload(t, store)
	require.NoError(t, store.MarkPackageError(id, "ffmpeg: boom"))
	// Asking again downloads it again, as for a download that failed.
	got := streamURL(t, env, u, id)
	assert.Equal(t, true, got["downloading"])
	assert.Equal(t, "Downloading the video again; preparing it failed: ffmpeg: boom", got["message"])
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)

	// A merged download from before tracks were kept apart still plays as it is.
	merged := seedAudioItem(t, store, "https://cdn.example.com/talk.mp4")
	_, err = store.DB().Exec(`UPDATE items SET type = 'video' WHERE id = ?`, merged)
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(merged))
	require.NoError(t, store.MarkDownloadReady(merged, "media/2.mp4", 1))
	require.NoError(t, store.MarkPackageError(merged, "ffmpeg: boom"))
	assert.Equal(t, map[string]any{"url": "/apps/feed/items/" + strconv.FormatInt(merged, 10) + "/media"}, streamURL(t, env, u, merged))
}

func TestPlayerFragmentHLS(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(id, 10), u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-hls="/apps/feed/hls/alice/`), "no data-hls")
	video := regexp.MustCompile(`<video[^>]*id="player-el"[^>]*>`).FindString(body)
	assert.True(t, video != "" && !strings.Contains(video, "src="), video)

	// One not packaged yet keeps playing its download.
	store := feedStore(t, env, "alice")
	plain := seedAudioItem(t, store, "https://cdn.example.com/talk.mp4")
	_, err := store.DB().Exec(`UPDATE items SET type = 'video' WHERE id = ?`, plain)
	require.NoError(t, err)
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(plain, 10), u.Auth).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "data-hls="), "data-hls on an unpackaged item")
	assert.True(t, strings.Contains(body, `src="/apps/feed/items/`+strconv.FormatInt(plain, 10)+`/media"`), "no src")
}

// A packaged video whose clean audio is not yet a track plays that audio
// from the media route; once the track exists it plays HLS.
func TestPlayerFragmentCleanPending(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	pending := packagedItem(t, env, "alice", true)
	require.NoError(t, store.SetCleanAudioPath(pending, "media/clean.m4a"))
	body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(pending, 10), u.Auth).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "data-hls="), "data-hls with the clean track pending")
	assert.True(t, strings.Contains(body, `src="/apps/feed/items/`+strconv.FormatInt(pending, 10)+`/media"`), "no media src")

	cleaned := cleanItem(t, env, "alice", true)
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(cleaned, 10), u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-hls="/apps/feed/hls/alice/`), "no data-hls for a clean track")
}

// A cleaned video streams HLS; until its clean track is packaged it streams
// the clean audio from the media route.
func TestStreamURLCleanedVideo(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", true)
	got := streamURL(t, env, u, id)
	hlsURL, _ := got["hls"].(string)
	assert.True(t, strings.HasPrefix(hlsURL, "/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/master.m3u8?sig="), got)

	pending := packagedItem(t, env, "alice", true)
	require.NoError(t, feedStore(t, env, "alice").SetCleanAudioPath(pending, "media/clean.m4a"))
	got = streamURL(t, env, u, pending)
	url, _ := got["url"].(string)
	assert.Equal(t, "/apps/feed/items/"+strconv.FormatInt(pending, 10)+"/media", url)
}
