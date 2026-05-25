package feed_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// seedReaderItems sets up:
//   - 2 folders: "FolderA", "FolderB"
//   - 3 subscriptions: subA1 in FolderA, subA2 in FolderA, subFree uncategorized
//   - 5 items: 2 read, 3 unread, 1 starred
//
// Returns folder A id and item IDs.
func seedReaderItems(t *testing.T, env *cogtest.Env, username string) (folderAID int64) {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	fp := raw.(*feed.Plugin)
	db, err := fp.DB(username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	folderAID, err = store.CreateFolder("FolderA")
	require.NoError(t, err)
	_, err = store.CreateFolder("FolderB")
	require.NoError(t, err)

	subA1ID, err := store.CreateSubscription(feed.Subscription{
		Kind:     "rss",
		URL:      "https://a1.example.com/feed",
		Title:    "SubA1",
		FolderID: sql.NullInt64{Int64: folderAID, Valid: true},
	})
	require.NoError(t, err)

	subA2ID, err := store.CreateSubscription(feed.Subscription{
		Kind:     "rss",
		URL:      "https://a2.example.com/feed",
		Title:    "SubA2",
		FolderID: sql.NullInt64{Int64: folderAID, Valid: true},
	})
	require.NoError(t, err)

	subFreeID, err := store.CreateSubscription(feed.Subscription{
		Kind:  "rss",
		URL:   "https://free.example.com/feed",
		Title: "SubFree",
	})
	require.NoError(t, err)

	// Item 0: unread, in subA1
	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subA1ID, GUID: "r0", Type: "text",
		Title: "UnreadA1Item1", ContentHTML: "<p>content a1-1</p>",
	})
	require.NoError(t, err)

	// Item 1: unread, in subA1, starred
	starredID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: subA1ID, GUID: "r1", Type: "text",
		Title: "StarredUnreadA1Item2", ContentHTML: "<p>starred</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkStarred(starredID, true))

	// Item 2: read, in subA2 (inserted as is_read=false, then marked read)
	readA2ID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: subA2ID, GUID: "r2", Type: "text",
		Title: "ReadA2Item1", ContentHTML: "<p>read</p>",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkRead(readA2ID, true))

	// Item 3: unread, in subFree
	_, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subFreeID, GUID: "r3", Type: "text",
		Title: "UnreadFreeItem1", ContentHTML: "<p>free</p>",
	})
	require.NoError(t, err)

	// Item 4: read, in subFree
	readFreeID, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: subFreeID, GUID: "r4", Type: "audio",
		Title: "ReadFreeAudioItem", MediaURL: "https://free.example.com/ep.mp3",
	})
	require.NoError(t, err)
	require.NoError(t, store.MarkRead(readFreeID, true))

	return folderAID
}

// TestFeedReaderHTTP verifies view pages and bulk-mark actions.
func TestFeedReaderHTTP(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "reader", "pass")

	folderAID := seedReaderItems(t, env, u.Username)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

	t.Run("IndexShowsAllArticlesHeader", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "All Articles") {
			t.Errorf("index page missing 'All Articles' header; body len=%d", len(body))
		}
	})

	t.Run("IndexSidebarShowsUnreadBadge", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		// 3 unread total: items 0, 1, 3
		if !strings.Contains(body, ">3<") && !strings.Contains(body, "badge\">3") {
			t.Logf("body sample: %q", body[:minInt(800, len(body))])
			t.Errorf("index sidebar missing total unread badge of 3")
		}
	})

	t.Run("IndexSidebarShowsStarredLink", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "Starred") {
			t.Errorf("index sidebar missing Starred link; body len=%d", len(body))
		}
	})

	t.Run("IndexSidebarShowsFolderA", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "FolderA") {
			t.Errorf("index sidebar missing FolderA; body len=%d", len(body))
		}
	})

	t.Run("FolderViewFiltersItems", func(t *testing.T) {
		fURL := "/apps/feed/folder/" + strconv.FormatInt(folderAID, 10)
		resp := env.Get(t, fURL, u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		// Items in FolderA: UnreadA1Item1, StarredUnreadA1Item2, ReadA2Item1
		if !strings.Contains(body, "UnreadA1Item1") {
			t.Errorf("folder page missing UnreadA1Item1; body len=%d", len(body))
		}
		if !strings.Contains(body, "StarredUnreadA1Item2") {
			t.Errorf("folder page missing StarredUnreadA1Item2; body len=%d", len(body))
		}
		// UnreadFreeItem1 is not in FolderA
		if strings.Contains(body, "UnreadFreeItem1") {
			t.Errorf("folder page should not show UnreadFreeItem1; body len=%d", len(body))
		}
	})

	t.Run("StarredViewFiltersItems", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/starred", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "StarredUnreadA1Item2") {
			t.Errorf("starred page missing StarredUnreadA1Item2; body len=%d", len(body))
		}
		if strings.Contains(body, "UnreadA1Item1") {
			t.Errorf("starred page should not show unstarred UnreadA1Item1; body len=%d", len(body))
		}
	})

	t.Run("MarkAllRead", func(t *testing.T) {
		postURL := "/apps/feed/items/read-all"
		env.PostForm(t, postURL, url.Values{"csrf_token": {csrfToken}}, sessionCookie, cogtest.StopRedirect)

		// Verify unread count is now 0.
		raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
		fp := raw.(*feed.Plugin)
		db, err := fp.DB(u.Username)
		require.NoError(t, err)
		store := feed.NewStore(db)
		n, err := store.CountUnread()
		require.NoError(t, err)
		if n != 0 {
			t.Errorf("after MarkAllRead, unread count = %d, want 0", n)
		}
	})
}

// TestFeedReaderBrowser verifies the reader UI in a real browser.
func TestFeedReaderBrowser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	u := env.CreateUser(t, "reader", "pass")

	folderAID := seedReaderItems(t, env, u.Username)

	b := eng.Start(t)
	b.Login(t, "reader", "pass")

	t.Run("SidebarShowsAllAndStarred", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "All Articles")
		b.WaitText(t, "Starred")
	})

	t.Run("SidebarShowsFolderA", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "FolderA")
	})

	t.Run("FolderView", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/folder/"+strconv.FormatInt(folderAID, 10))
		b.WaitText(t, "FolderA")
		b.WaitText(t, "UnreadA1Item1")
	})

	t.Run("StarredView", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/starred")
		b.WaitText(t, "Starred")
		b.WaitText(t, "StarredUnreadA1Item2")
	})
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
