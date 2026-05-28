package feed_test

import (
	"database/sql"
	"fmt"
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

func TestHideReadFiltersIndexList(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "TestFeed"})
	require.NoError(t, err)
	readID, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g-read", Type: "text", Title: "ALPHA-READ-TITLE"})
	require.NoError(t, err)
	require.NoError(t, s.MarkRead(readID, true))
	_, _, err = s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "g-unread", Type: "text", Title: "BETA-UNREAD-TITLE"})
	require.NoError(t, err)
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))
	resp := env.Get(t, "/apps/feed/", u.Auth)
	resp.Status(200)
	resp.Contains("BETA-UNREAD-TITLE")
	if strings.Contains(resp.ReadBody(), "ALPHA-READ-TITLE") {
		t.Fatal("read item should be hidden when HideEmpty enabled")
	}
}

// TestHideReadAppliesInSQL seeds 600 read items (newest published_at) and one
// unread item with published_at=1 (oldest). With feedViewLimit=500 a Go-side
// filter would only see the newest 500 rows — all read — and miss the unread
// item. With the SQL-side "is_read=0" filter, SQLite skips the reads and
// returns the unread item directly, so it must appear on the index page.
func TestHideReadAppliesInSQL(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)

	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/hideread", Title: "HideReadFeed"})
	require.NoError(t, err)

	// Insert 600 read items with descending published_at (newest first).
	const readCount = 600
	for i := readCount; i >= 1; i-- {
		id, _, err := s.UpsertItem(feed.Item{
			SubscriptionID: subID,
			GUID:           "r" + strconv.Itoa(i),
			Type:           "text",
			Title:          "ReadItem-" + strconv.Itoa(i),
			PublishedAt:    int64(i + 1), // published_at >= 2
		})
		require.NoError(t, err)
		require.NoError(t, s.MarkRead(id, true))
	}

	// Insert the one unread item with the oldest published_at=1.
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "unread-oldest",
		Type:           "text",
		Title:          "UNREAD-NEEDLE",
		PublishedAt:    1,
	})
	require.NoError(t, err)

	// Enable HideEmpty (hide-read) preference.
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))

	// GET /apps/feed/ — must contain the unread item despite it being beyond
	// the feedViewLimit when reads are sorted first.
	resp := env.Get(t, "/apps/feed/", u.Auth)
	resp.Status(200)
	resp.Contains("UNREAD-NEEDLE")
}

func TestIndexSearchFindsItemsPastLimit(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	subID, err := s.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://x.test/f", Title: "S"})
	require.NoError(t, err)
	// 600 noise items (newer), 1 matching "needle" item (oldest).
	for i := range 600 {
		_, _, err = s.UpsertItem(feed.Item{
			SubscriptionID: subID,
			GUID:           "n" + strconv.Itoa(i),
			Type:           "text",
			Title:          "noise " + strconv.Itoa(i),
			PublishedAt:    int64(1000 + i),
		})
		require.NoError(t, err)
	}
	_, _, err = s.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "needle",
		Type:           "text",
		Title:          "UNIQUE-NEEDLE-XYZ",
		PublishedAt:    1, // oldest — far past the 500-row newest-first cut
	})
	require.NoError(t, err)
	resp := env.Get(t, "/apps/feed/?q=UNIQUE-NEEDLE-XYZ", u.Auth)
	resp.Status(200)
	// Title appears in the rendered row as `>UNIQUE-NEEDLE-XYZ<` (between
	// tags). The search input echoes the query but as `value="..."`, so the
	// `>...<` form is a robust signal the item was actually selected.
	resp.Contains(">UNIQUE-NEEDLE-XYZ<")
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

func TestItemDetailNavSurvivesReadAndHideRead(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	if !ok {
		t.Fatal("feed plugin not registered")
	}
	db, err := raw.(*feed.Plugin).DB(u.User.Username)
	require.NoError(t, err)
	s := feed.NewStore(db)
	fid, err := s.CreateFolder("Dev")
	require.NoError(t, err)
	subID, err := s.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://x.test/f", Title: "S",
		FolderID: sql.NullInt64{Int64: fid, Valid: true},
	})
	require.NoError(t, err)
	// Three items in the folder; opening the middle one in hide-read mode
	// should still show a working prev/next.
	id1, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "a", Type: "text", Title: "first",  PublishedAt: 1})
	require.NoError(t, err)
	id2, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "b", Type: "text", Title: "middle", PublishedAt: 2})
	require.NoError(t, err)
	id3, _, err := s.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "c", Type: "text", Title: "last",   PublishedAt: 3})
	require.NoError(t, err)
	p, err := s.LoadPlayerPrefs()
	require.NoError(t, err)
	p.HideEmpty = true
	require.NoError(t, s.SavePlayerPrefs(p))

	// Open the middle item from the folder context.
	url := fmt.Sprintf("/apps/feed/items/%d?ctx=folder&cid=%d", id2, fid)
	resp := env.Get(t, url, u.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	// Prev/Next must be enabled — i.e. the bar contains anchor links to the
	// neighbor IDs, not the disabled spans (`opacity:0.5`).
	// ItemHref shape is /apps/feed/items/{id}?ctx=...
	prevHref := fmt.Sprintf(`href="/apps/feed/items/%d?ctx=folder`, id1)
	nextHref := fmt.Sprintf(`href="/apps/feed/items/%d?ctx=folder`, id3)
	if !strings.Contains(body, prevHref) {
		t.Errorf("Prev link missing (%s); body excerpt: %q", prevHref, snippet(body, "feed-context-bar"))
	}
	if !strings.Contains(body, nextHref) {
		t.Errorf("Next link missing (%s); body excerpt: %q", nextHref, snippet(body, "feed-context-bar"))
	}
	_, _ = id2, fid
}

// snippet returns ~300 chars of body around the first occurrence of marker, or "".
func snippet(body, marker string) string {
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	end := i + 400
	if end > len(body) {
		end = len(body)
	}
	return body[i:end]
}
