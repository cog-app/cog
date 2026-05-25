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
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// seedFeedItems creates a subscription and two items (text + audio) in the user's
// feed store. Returns the item IDs.
func seedFeedItems(t *testing.T, env *cogtest.Env, username string) (textID, audioID int64) {
	t.Helper()
	p, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	fp, ok := p.(*feed.Plugin)
	require.True(t, ok, "plugin is not *feed.Plugin")
	db, err := fp.DB(username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x", Title: "Daily"})
	require.NoError(t, err)

	textID, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g1", Type: "text",
		Title: "TextItem", ContentHTML: "<p>body</p>",
	})
	require.NoError(t, err)

	audioID, _, err = store.UpsertItem(feed.Item{
		SubscriptionID: subID, GUID: "g2", Type: "audio",
		Title: "AudioItem", MediaURL: "https://x/a.mp3",
	})
	require.NoError(t, err)

	return textID, audioID
}

func TestFeedIndex_HTTP(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")

	textID, audioID := seedFeedItems(t, env, u.Username)

	t.Run("ListsAllItems", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "TextItem") {
			t.Errorf("index page missing TextItem; got body len=%d", len(body))
		}
		if !strings.Contains(body, "AudioItem") {
			t.Errorf("index page missing AudioItem; got body len=%d", len(body))
		}
	})

	t.Run("FilterByType", func(t *testing.T) {
		resp := env.Get(t, "/apps/feed/?q=type:audio", u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "AudioItem") {
			t.Errorf("filtered page missing AudioItem; got body len=%d", len(body))
		}
		if strings.Contains(body, "TextItem") {
			t.Errorf("filtered page should not contain TextItem; got body len=%d", len(body))
		}
	})

	t.Run("ItemDetailMarksRead", func(t *testing.T) {
		url := "/apps/feed/items/" + strconv.FormatInt(textID, 10)
		resp := env.Get(t, url, u.Auth)
		resp.Status(http.StatusOK)
		body := resp.ReadBody()
		if !strings.Contains(body, "TextItem") {
			t.Errorf("detail page missing title; got body len=%d", len(body))
		}
		if !strings.Contains(body, "<p>body</p>") {
			t.Errorf("detail page missing content HTML; got body len=%d", len(body))
		}

		// Verify item is now marked read.
		p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
		fp := p.(*feed.Plugin)
		db, err := fp.DB(u.Username)
		require.NoError(t, err)
		store := feed.NewStore(db)
		it, err := store.GetItem(textID)
		require.NoError(t, err)
		if !it.IsRead {
			t.Errorf("item should be marked read after visiting detail page")
		}
	})

	t.Run("ItemMedia_RedirectsToMediaURL", func(t *testing.T) {
		url := "/apps/feed/items/" + strconv.FormatInt(audioID, 10) + "/media"
		resp := env.Get(t, url, u.Auth, cogtest.StopRedirect)
		resp.Status(http.StatusFound)
		loc := resp.Header.Get("Location")
		if loc != "https://x/a.mp3" {
			t.Errorf("media redirect = %q, want https://x/a.mp3", loc)
		}
	})

	t.Run("ToggleRead", func(t *testing.T) {
		// Create a session so CSRF validation passes.
		sess, err := env.Providers.Sessions.Create(u.ID)
		require.NoError(t, err)
		sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
		csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

		// Get initial read state via store.
		p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
		fp := p.(*feed.Plugin)
		db, _ := fp.DB(u.Username)
		store := feed.NewStore(db)
		it, err := store.GetItem(textID)
		require.NoError(t, err)
		before := it.IsRead

		postURL := "/apps/feed/items/" + strconv.FormatInt(textID, 10) + "/read"
		env.PostForm(t, postURL, url.Values{"csrf_token": {csrfToken}}, sessionCookie)

		it2, err := store.GetItem(textID)
		require.NoError(t, err)
		if it2.IsRead == before {
			t.Errorf("ToggleRead did not change read state; was %v, still %v", before, it2.IsRead)
		}
	})

	t.Run("ToggleStar", func(t *testing.T) {
		sess, err := env.Providers.Sessions.Create(u.ID)
		require.NoError(t, err)
		sessionCookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
		csrfToken := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)

		p, _ := env.Providers.Plugins.Plugin(feed.PluginID)
		fp := p.(*feed.Plugin)
		db, _ := fp.DB(u.Username)
		store := feed.NewStore(db)
		it, err := store.GetItem(textID)
		require.NoError(t, err)
		before := it.IsStarred

		postURL := "/apps/feed/items/" + strconv.FormatInt(textID, 10) + "/star"
		env.PostForm(t, postURL, url.Values{"csrf_token": {csrfToken}}, sessionCookie)

		it2, err := store.GetItem(textID)
		require.NoError(t, err)
		if it2.IsStarred == before {
			t.Errorf("ToggleStar did not change starred state; was %v, still %v", before, it2.IsStarred)
		}
	})
}

func TestFeedIndex_Browser(t *testing.T) {
	env := cogtest.New(t, feed.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	u := env.CreateUser(t, "u", "pass")

	seedFeedItems(t, env, u.Username)

	b := eng.Start(t)
	b.Login(t, "u", "pass")

	t.Run("ListsAllItems", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/")
		b.WaitText(t, "TextItem")
		b.WaitText(t, "AudioItem")
	})

	t.Run("FilterByType", func(t *testing.T) {
		b.Navigate(t, "/apps/feed/?q=type:audio")
		b.WaitText(t, "AudioItem")
		// TextItem should not appear in audio-filtered view.
	})
}
