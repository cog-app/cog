package feed_test

import (
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

// postItemDownload posts the download action for itemID as user and returns
// the response, without following the redirect.
func postItemDownload(t *testing.T, env *cogtest.Env, u *cogtest.User, itemID int64, query string) *cogtest.Response {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	path := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/download"
	if query != "" {
		path += "?" + query
	}
	return env.PostForm(t, path,
		url.Values{"csrf_token": {auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)}},
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID),
		cogtest.StopRedirect,
	)
}

func TestItemDownloadQueuesAndShowsPreparing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	page := "/apps/feed/items/" + strconv.FormatInt(itemID, 10)

	env.Get(t, page, u.Auth).Status(http.StatusOK).Contains(page + "/download")

	resp := postItemDownload(t, env, u, itemID, "ctx=starred")
	resp.Status(http.StatusSeeOther)
	assert.Equal(t, page+"?ctx=starred", resp.Header.Get("Location"))

	d, err := store.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)

	env.Get(t, page, u.Auth).Status(http.StatusOK).
		Contains(`data-download-state="queued"`).
		NotContains(page + "/download")
}

func TestItemDownloadRetryClearsFailure(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "bob", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	page := "/apps/feed/items/" + strconv.FormatInt(itemID, 10)

	require.NoError(t, store.EnqueueDownload(itemID))
	require.NoError(t, store.MarkDownloadStart(itemID))
	require.NoError(t, store.MarkDownloadError(itemID, "404 <Not Found>"))

	// The failure and its reason are shown, escaped, with a way to retry.
	env.Get(t, page, u.Auth).Status(http.StatusOK).
		Contains(`data-download-state="error"`).
		Contains(`title="404 &lt;Not Found&gt;"`).
		Contains(page + "/download")

	postItemDownload(t, env, u, itemID, "").Status(http.StatusSeeOther)

	d, err := store.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)
	assert.Equal(t, "", d.Error)
}

func TestItemDownloadLeavesRunningAndReadyAlone(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "carol", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")

	require.NoError(t, store.EnqueueDownload(itemID))
	require.NoError(t, store.MarkDownloadStart(itemID))
	postItemDownload(t, env, u, itemID, "").Status(http.StatusSeeOther)
	d, err := store.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "downloading", d.State)

	require.NoError(t, store.MarkDownloadReady(itemID, "media/1.mp3", 10))
	postItemDownload(t, env, u, itemID, "").Status(http.StatusSeeOther)
	d, err = store.GetDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "ready", d.State)
	assert.Equal(t, "media/1.mp3", d.LocalPath)
}

// Download state lives in each user's database, so one user's download of
// an item says nothing about another user's item with the same ID.
func TestItemDownloadIsPerUser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	aliceStore := feedStore(t, env, alice.Username)
	bobStore := feedStore(t, env, bob.Username)
	aliceItem := seedAudioItem(t, aliceStore, "https://cdn.example.com/a.mp3")
	bobItem := seedAudioItem(t, bobStore, "https://cdn.example.com/b.mp3")
	require.Equal(t, aliceItem, bobItem)

	postItemDownload(t, env, alice, aliceItem, "").Status(http.StatusSeeOther)

	page := "/apps/feed/items/" + strconv.FormatInt(bobItem, 10)
	env.Get(t, page, bob.Auth).Status(http.StatusOK).
		NotContains(`data-download-state=`).
		Contains(page + "/download")
}

func TestItemDownloadRefusesTextItems(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "dave", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://blog.example.com/feed"})
	require.NoError(t, err)
	itemID, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "post", Type: "text", Title: "Post"})
	require.NoError(t, err)

	postItemDownload(t, env, u, itemID, "").Status(http.StatusSeeOther)
	d, err := store.FindDownload(itemID)
	require.NoError(t, err)
	assert.Equal(t, "", d.State)
}

// A downloaded file is served as itself, ranges included: it is the media
// element's source and a native client's download, never a page. Registered
// as a page, it went through the shell, which wraps any status below 300.
func TestItemMediaServesTheDownloadedFile(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "dave", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")

	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	space, err := raw.(*feed.Plugin).Space(u.Username)
	require.NoError(t, err)
	n, err := space.Own.Put("media/1.mp3", strings.NewReader("0123456789"))
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(itemID))
	require.NoError(t, store.MarkDownloadStart(itemID))
	require.NoError(t, store.MarkDownloadReady(itemID, "media/1.mp3", n))

	path := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/media"
	env.Get(t, path, u.Auth).Status(http.StatusOK).Equals("0123456789")
	env.Get(t, path, u.Auth, cogtest.Header("Range", "bytes=2-5")).Status(http.StatusPartialContent).Equals("2345")
}

// A client without a session, a native app or a media player, is told it is
// not signed in rather than sent to the login page.
func TestItemMediaRefusesWithoutCredentials(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "erin", "pass")
	itemID := seedAudioItem(t, feedStore(t, env, u.Username), "https://cdn.example.com/ep1.mp3")
	env.Get(t, "/apps/feed/items/"+strconv.FormatInt(itemID, 10)+"/media", cogtest.StopRedirect).Status(http.StatusUnauthorized)
}

// An item the user cleaned never plays its unclean audio: when the clean file
// is missing the request fails instead of falling back to the download.
func TestItemMediaMissingCleanFileFailsClosed(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "frank", "pass")
	store := feedStore(t, env, u.Username)
	itemID := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	space, err := raw.(*feed.Plugin).Space(u.Username)
	require.NoError(t, err)
	n, err := space.Own.Put("media/1.mp3", strings.NewReader("unclean"))
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(itemID))
	require.NoError(t, store.MarkDownloadStart(itemID))
	require.NoError(t, store.MarkDownloadReady(itemID, "media/1.mp3", n))
	require.NoError(t, store.SetCleanAudioPath(itemID, "media/1.clean.m4a"))

	path := "/apps/feed/items/" + strconv.FormatInt(itemID, 10) + "/media"
	env.Get(t, path, u.Auth).Status(http.StatusServiceUnavailable)
}
