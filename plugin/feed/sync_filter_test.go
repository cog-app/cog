package feed_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/syncprefs"
)

// TestSyncFilters checks that each sync protocol exposes only what its
// filter lets through, and that a change of filter reaches clients that ask
// only for changes.
func TestSyncFilters(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	session := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	save := func(ncnews, gpodder string) *cogtest.Response {
		t.Helper()
		return env.PostForm(t, "/apps/feed/settings/sync",
			url.Values{"csrf_token": {csrf}, "ncnews": {ncnews}, "gpodder": {gpodder}},
			session, cogtest.StopRedirect).Status(http.StatusSeeOther)
	}

	podcasts, err := store.CreateFolder("Podcasts")
	require.NoError(t, err)
	sub := func(rawURL string, folder sql.NullInt64) int64 {
		t.Helper()
		var id int64
		require.NoError(t, db.QueryRow(`INSERT INTO subscriptions (kind, url, title, folder_id) VALUES ('rss', ?1, ?1, ?2) RETURNING id`,
			rawURL, folder).Scan(&id))
		return id
	}
	item := func(subID int64, guid, typ, mediaURL, content string) int64 {
		t.Helper()
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: guid, Type: typ, Title: guid, MediaURL: mediaURL, ContentHTML: content})
		require.NoError(t, err)
		return id
	}
	blog := sub("https://blog.example/rss", sql.NullInt64{})
	item(blog, "post1", "text", "", "<p>post</p>")
	pod := sub("https://pod.example/feed.xml", sql.NullInt64{Int64: podcasts, Valid: true})
	episode := item(pod, "ep1", "audio", "https://pod.example/ep1.mp3", "<p>notes</p>")
	other := sub("https://other.example/feed.xml", sql.NullInt64{})
	item(other, "x1", "audio", "https://other.example/x1.mp3", "")

	newsFeeds := func() map[string]int {
		t.Helper()
		var resp struct {
			Feeds []struct {
				URL         string `json:"url"`
				UnreadCount int    `json:"unreadCount"`
			} `json:"feeds"`
		}
		env.Get(t, "/index.php/apps/news/api/v1-2/feeds", u.Auth).Status(200).JSON(&resp)
		out := map[string]int{}
		for _, f := range resp.Feeds {
			out[f.URL] = f.UnreadCount
		}
		return out
	}
	newsItems := func(path string) []string {
		t.Helper()
		var resp struct {
			Items []struct {
				GUID string `json:"guid"`
			} `json:"items"`
		}
		env.Get(t, "/index.php/apps/news/api/v1-2/"+path, u.Auth).Status(200).JSON(&resp)
		var out []string
		for _, it := range resp.Items {
			out = append(out, it.GUID)
		}
		return out
	}
	type subscriptionChanges struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	podcastSubs := func(since int64) subscriptionChanges {
		t.Helper()
		var got subscriptionChanges
		env.Get(t, "/index.php/apps/gpoddersync/subscriptions?since="+strconv.FormatInt(since, 10), u.Auth).Status(200).JSON(&got)
		return got
	}

	// Without filters: every feed in News, feeds with episodes in GPodder.
	require.Equal(t, map[string]int{"https://blog.example/rss": 1, "https://pod.example/feed.xml": 1, "https://other.example/feed.xml": 1}, newsFeeds())
	require.Equal(t, []string{"https://pod.example/feed.xml", "https://other.example/feed.xml"}, podcastSubs(0).Add)

	t.Run("RejectsBadQuery", func(t *testing.T) {
		save("(((", "folder:Podcasts")
		for _, p := range syncprefs.Protocols {
			q, _, err := syncprefs.Stored(db, p)
			require.NoError(t, err)
			require.Equal(t, "", q)
		}
	})

	save("NOT folder:Podcasts", "folder:Podcasts")
	_, changedAt, err := syncprefs.Stored(db, syncprefs.GPodder)
	require.NoError(t, err)
	require.True(t, changedAt > 0)

	t.Run("NewsSeesOnlyMatchingArticles", func(t *testing.T) {
		require.Equal(t, map[string]int{"https://blog.example/rss": 1, "https://other.example/feed.xml": 1}, newsFeeds())
		require.Equal(t, []string{"x1", "post1"}, newsItems("items?type=3&batchSize=-1"))

		// Marking everything read leaves alone what the client never saw.
		env.Do(t, cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+"/index.php/apps/news/api/v1-2/items/read", nil), u.Auth).Status(200)
		it, err := store.GetItem(episode)
		require.NoError(t, err)
		require.False(t, it.IsRead)
	})

	t.Run("PodcastAppSeesOnlyMatchingFeeds", func(t *testing.T) {
		require.Equal(t, []string{"https://pod.example/feed.xml"}, podcastSubs(0).Add)
		// A client that synced before the change gets the whole state, with
		// what the filter now holds back as removed.
		got := podcastSubs(changedAt - 1)
		require.Equal(t, []string{"https://pod.example/feed.xml"}, got.Add)
		require.Equal(t, []string{"https://blog.example/rss", "https://other.example/feed.xml"}, got.Remove)
		// One that synced since gets only changes.
		got = podcastSubs(changedAt + 1)
		require.Equal(t, []string{}, got.Add)
		require.Equal(t, []string{}, got.Remove)
	})

	t.Run("WideningReachesNewsClients", func(t *testing.T) {
		_, err := db.Exec(`UPDATE items SET fetched_at = 1`)
		require.NoError(t, err)
		save("", "folder:Podcasts")
		require.Equal(t, []string{"ep1"}, newsItems("items/updated?type=3&lastModified=100"))
		require.Equal(t, map[string]int{"https://blog.example/rss": 0, "https://pod.example/feed.xml": 1, "https://other.example/feed.xml": 0}, newsFeeds())
	})

	// An episode is one item whatever app reads it, so its type keeps
	// podcasts out of News without putting them in a folder.
	t.Run("TypeKeepsPodcastsOut", func(t *testing.T) {
		save("NOT type:audio", "")
		require.Equal(t, map[string]int{"https://blog.example/rss": 0}, newsFeeds())
		require.Equal(t, []string{"post1"}, newsItems("items?type=3&batchSize=-1"))
	})
}
