package feed_test

import (
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

var codeRE = regexp.MustCompile(`<code class="break-anywhere">(http[^<]*\.m3u\?[^<]*)</code>`)

func TestViewPagesOfferM3U(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")

	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/m3u", Title: "Pod"})
	require.NoError(t, err)
	item, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "ep", Type: "audio", Title: "Ep",
		MediaURL: "https://cdn.example.com/ep.mp3", DurationSec: 60})
	require.NoError(t, err)
	require.NoError(t, store.MarkStarred(item, true))
	require.NoError(t, store.AddTag(item, "a b"))
	pl, err := store.CreatePlaylist(feed.Playlist{Name: "Everything"})
	require.NoError(t, err)
	folder, err := store.CreateFolder("Folder")
	require.NoError(t, err)

	id := strconv.FormatInt
	pages := []struct{ page, list string }{
		{"/apps/feed/playlists/" + id(pl, 10), "playlist/" + id(pl, 10)},
		{"/apps/feed/folder/" + id(folder, 10), "folder/" + id(folder, 10)},
		{"/apps/feed/subscription/" + id(sub, 10), "sub/" + id(sub, 10)},
		{"/apps/feed/tag/" + url.PathEscape("a b"), "tag/" + url.PathEscape("a b")},
		{"/apps/feed/starred", "starred/all"},
		{"/apps/feed/", "unread/all"},
	}
	for _, tc := range pages {
		t.Run(tc.page, func(t *testing.T) {
			body := env.Get(t, tc.page, alice.Auth).Status(http.StatusOK).ReadBody()
			prefix := "/apps/feed/m3u/alice/" + tc.list + ".m3u?sig="
			start := strings.Index(body, prefix)
			require.True(t, start >= 0, "%q not in body", prefix)
			// The first link is the plain one; its skip twin follows.
			end := strings.IndexAny(body[start:], `"<`)
			link := html.UnescapeString(body[start : start+end])
			assert.True(t, strings.Contains(body, env.Providers.Config.BaseURL+prefix), "link is not absolute")
			// The second link is the same list with silence skipped.
			var shown []string
			for _, m := range codeRE.FindAllStringSubmatch(body, -1) {
				shown = append(shown, html.UnescapeString(m[1]))
			}
			require.Equal(t, 2, len(shown), "%v", shown)
			assert.True(t, strings.HasSuffix(shown[1], "&skip=silence"), shown[1])
			assert.True(t, strings.Contains(shown[1], prefix), shown[1])

			env.Get(t, relative(env, link)).Status(http.StatusOK)
		})
	}
}

// The index page's links play the unread list, say so, and are not offered
// beside a search, which the M3U cannot carry.
func TestIndexPageM3ULabelAndSearch(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	mediaItem(t, store, "audio", "Ep", "https://cdn.example.com/ep.mp3")

	body := env.Get(t, "/apps/feed/", alice.Auth).Status(http.StatusOK).ReadBody()
	assert.Contains(t, body, "<summary>Play unread in mpv</summary>")
	assert.Contains(t, body, "/apps/feed/m3u/alice/unread/all.m3u?sig=")

	body = env.Get(t, "/apps/feed/?q=Ep", alice.Auth).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "in mpv"), "box shown beside a search")
	assert.False(t, strings.Contains(body, "/m3u/"), "M3U link shown beside a search")
}
