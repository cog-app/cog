package feed_test

import (
	"html"
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

// m3uURL signs username's list at path (kind/<unescaped id>.m3u) through
// the plugin, as the pages do.
func m3uURL(t *testing.T, env *cogtest.Env, username, path string, skip bool) string {
	t.Helper()
	kind, file, ok := strings.Cut(path, "/")
	require.True(t, ok, path)
	l := feed.M3UList{Kind: feed.ViewKind(kind), ID: strings.TrimSuffix(file, ".m3u")}
	u, err := feedPlugin(t, env).M3UURL(feedStore(t, env, username), username, l, feed.HLSOptions{Skip: skip})
	require.NoError(t, err)
	return u
}

// lineAfterTitle is the URL line of the entry whose title ends in title.
func lineAfterTitle(body, title string) string {
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "#EXTINF:") && strings.HasSuffix(l, title) && i+1 < len(lines) {
			return lines[i+1]
		}
	}
	return ""
}

// entryURLs are the URL lines of an M3U, in order.
func entryURLs(body string) []string {
	var out []string
	for l := range strings.SplitSeq(body, "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// relative is u without the test server's base, for env.Get.
func relative(env *cogtest.Env, u string) string {
	return strings.TrimPrefix(u, env.Providers.Config.BaseURL)
}

// podcastEpisode stores an undownloaded podcast episode in a subscription
// titled "Pod", and returns its ID.
func podcastEpisode(t *testing.T, store *feed.Store, title string) int64 {
	t.Helper()
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/" + strconv.Itoa(nextFeed()), Title: "Pod"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: title, Type: "audio", Title: title,
		MediaURL: "https://cdn.example.com/ep.mp3", DurationSec: 1800})
	require.NoError(t, err)
	return id
}

// mixedPlaylist stores alice's packaged video, podcast episode "Talk" and a
// text item, and a playlist of everything; it returns the playlist's ID and
// the packaged item's.
func mixedPlaylist(t *testing.T, env *cogtest.Env) (int64, int64) {
	t.Helper()
	store := feedStore(t, env, "alice")
	packaged := packagedItem(t, env, "alice", true)
	podcastEpisode(t, store, "Talk")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://blog.example.com/" + strconv.Itoa(nextFeed())})
	require.NoError(t, err)
	_, _, err = store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "post", Type: "text", Title: "Post", URL: "https://blog.example.com/post"})
	require.NoError(t, err)
	pl, err := store.CreatePlaylist(feed.Playlist{Name: "Everything"})
	require.NoError(t, err)
	return pl, packaged
}

func TestM3UPlaylist(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	pl, _ := mixedPlaylist(t, env)

	r := env.Get(t, m3uURL(t, env, "alice", "playlist/"+strconv.FormatInt(pl, 10)+".m3u", false), cogtest.StopRedirect).Status(http.StatusOK)
	assert.Equal(t, "audio/x-mpegurl", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
	assert.Equal(t, "", r.Header.Get("Set-Cookie"))
	body := r.ReadBody()
	assert.True(t, strings.HasPrefix(body, "#EXTM3U\n"), body)
	assert.Equal(t, 2, strings.Count(body, "#EXTINF:"), body)
	assert.True(t, strings.Contains(body, "#EXTINF:1800,Pod – Talk\nhttps://cdn.example.com/ep.mp3\n"), body)
	master := lineAfterTitle(body, "– Episode")
	assert.True(t, strings.HasPrefix(master, env.Providers.Config.BaseURL+"/apps/feed/hls/alice/"), master)
	assert.True(t, strings.Contains(body, "#EXTINF:-1,"), body)
	env.Get(t, relative(env, master), cogtest.StopRedirect).Status(http.StatusOK)
}

func TestM3USkipSilence(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	pl, packaged := mixedPlaylist(t, env)
	path := "playlist/" + strconv.FormatInt(pl, 10) + ".m3u"
	poked(t, env)

	plain := env.Get(t, m3uURL(t, env, "alice", path, false)).Status(http.StatusOK).ReadBody()
	assert.False(t, download(t, env, "alice", packaged).EdgesWanted)
	assert.False(t, poked(t, env))
	assert.False(t, strings.Contains(plain, "skip="), plain)

	body := env.Get(t, m3uURL(t, env, "alice", path, true)).Status(http.StatusOK).ReadBody()
	assert.True(t, download(t, env, "alice", packaged).EdgesWanted)
	assert.True(t, poked(t, env))
	assert.True(t, strings.Contains(body, "#EXTINF:1800,Pod – Talk\nhttps://cdn.example.com/ep.mp3\n"), body)
	master := lineAfterTitle(body, "– Episode")
	assert.True(t, strings.Contains(master, "&skip=silence"), master)
	assert.False(t, strings.Contains(master, "edit="), master)
	env.Get(t, relative(env, master)).Status(http.StatusOK)

	// Asking again within wantedRefresh writes nothing and pokes no one.
	env.Get(t, m3uURL(t, env, "alice", path, true)).Status(http.StatusOK)
	assert.False(t, poked(t, env))
}

func TestM3URejects(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	env.CreateUser(t, "bob", "pw")
	pl, packaged := mixedPlaylist(t, env)
	path := "playlist/" + strconv.FormatInt(pl, 10) + ".m3u"
	good := m3uURL(t, env, "alice", path, false)
	m3uURL(t, env, "bob", "starred/all.m3u", false) // bob has a key
	sig := strings.TrimPrefix(good[strings.Index(good, "?"):], "?sig=")
	flipped := []byte(sig)
	flipped[0] = 'A'
	if sig[0] == 'A' {
		flipped[0] = 'B'
	}
	hlsSig := masterURL(t, env, "alice", packaged)
	hlsSig = hlsSig[strings.Index(hlsSig, "?sig=")+len("?sig="):]
	n := strconv.FormatInt(packaged, 10)

	for name, c := range map[string]struct {
		url  string
		want int
	}{
		"history":      {"/apps/feed/m3u/alice/history/all.m3u?sig=" + sig, http.StatusBadRequest},
		"m3u8":         {"/apps/feed/m3u/alice/playlist/7.m3u8?sig=" + sig, http.StatusBadRequest},
		"non-numeric":  {"/apps/feed/m3u/alice/playlist/x.m3u?sig=" + sig, http.StatusBadRequest},
		"extra":        {good + "&extra=1", http.StatusBadRequest},
		"skip fast":    {good + "&skip=fast", http.StatusBadRequest},
		"edit":         {good + "&skip=silence&edit=0123456789ab", http.StatusBadRequest},
		"sig flipped":  {"/apps/feed/m3u/alice/" + path + "?sig=" + string(flipped), http.StatusForbidden},
		"no sig":       {"/apps/feed/m3u/alice/" + path, http.StatusForbidden},
		"bob":          {"/apps/feed/m3u/bob/" + path + "?sig=" + sig, http.StatusForbidden},
		"nobody":       {"/apps/feed/m3u/nobody/" + path + "?sig=" + sig, http.StatusForbidden},
		"no playlist":  {m3uURL(t, env, "alice", "playlist/999999.m3u", false), http.StatusNotFound},
		"skip added":   {good + "&skip=silence", http.StatusForbidden},
		"hls sig":      {"/apps/feed/m3u/alice/playlist/" + n + ".m3u?sig=" + hlsSig, http.StatusForbidden},
		"other list":   {"/apps/feed/m3u/alice/starred/all.m3u?sig=" + sig, http.StatusForbidden},
		"another kind": {"/apps/feed/m3u/alice/folder/" + strconv.FormatInt(pl, 10) + ".m3u?sig=" + sig, http.StatusForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			env.Get(t, c.url, cogtest.StopRedirect).Status(c.want)
		})
	}
}

// Serving an M3U only reads the key: a request for a user who never signed
// one finds none, is refused, and writes nothing.
func TestM3UNoKeyWritesNothing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "carol", "pw")
	env.Get(t, "/apps/feed/m3u/carol/starred/all.m3u?sig=x", cogtest.StopRedirect).Status(http.StatusForbidden)
	key, err := feedStore(t, env, "carol").ExistingHLSKey()
	require.NoError(t, err)
	assert.True(t, key == nil, "key created")
}

func TestM3URevoke(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	pl, _ := mixedPlaylist(t, env)
	path := "playlist/" + strconv.FormatInt(pl, 10) + ".m3u"
	old := m3uURL(t, env, "alice", path, false)
	env.Get(t, old).Status(http.StatusOK)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	session := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	env.PostForm(t, "/apps/feed/settings/media-links/revoke", url.Values{"csrf_token": {csrf}},
		session, cogtest.StopRedirect).Status(http.StatusSeeOther)

	env.Get(t, old).Status(http.StatusForbidden)
	body := env.Get(t, m3uURL(t, env, "alice", path, false)).Status(http.StatusOK).ReadBody()
	env.Get(t, relative(env, lineAfterTitle(body, "– Episode"))).Status(http.StatusOK)
}

func TestM3UOrderMatchesPage(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://podcast.example.com/" + strconv.Itoa(nextFeed())})
	require.NoError(t, err)
	for i, at := range []int64{2000, 1000, 3000} {
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "ep" + strconv.Itoa(i), Type: "audio", Title: "Ep",
			MediaURL: "https://cdn.example.com/ep.mp3", PublishedAt: at})
		require.NoError(t, err)
		packageItem(t, env, "alice", id, "audio.m4a", "")
	}
	for _, o := range []feed.Playlist{
		{Name: "Oldest", Order: feed.OrderOldest},
		{Name: "Shuffled", Order: feed.OrderShuffled, ShuffleSeed: 42},
	} {
		t.Run(o.Name, func(t *testing.T) {
			pl, err := store.CreatePlaylist(o)
			require.NoError(t, err)
			spec, expr, err := store.ResolveView(feed.View{Kind: feed.ViewPlaylist, ID: pl})
			require.NoError(t, err)
			items, _, err := store.ViewPage(spec, expr, env.Clock.Now(), 500)
			require.NoError(t, err)
			var want []string
			for _, it := range items {
				want = append(want, "/apps/feed/hls/alice/"+strconv.FormatInt(it.ID, 10)+"/")
			}
			body := env.Get(t, m3uURL(t, env, "alice", "playlist/"+strconv.FormatInt(pl, 10)+".m3u", false)).Status(http.StatusOK).ReadBody()
			got := entryURLs(body)
			require.Equal(t, len(want), len(got), body)
			for i := range want {
				assert.True(t, strings.HasPrefix(got[i], env.Providers.Config.BaseURL+want[i]), "entry %d: %s, want %s", i, got[i], want[i])
			}
		})
	}
}

func TestM3UTag(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	tag := "a b/c%d é.m3u"
	id := packagedItem(t, env, "alice", true)
	packagedItem(t, env, "alice", true) // untagged
	require.NoError(t, feedStore(t, env, "alice").AddTag(id, tag))
	u := m3uURL(t, env, "alice", "tag/"+tag+".m3u", false)
	body := env.Get(t, u).Status(http.StatusOK).ReadBody()
	got := entryURLs(body)
	require.Equal(t, 1, len(got), body)
	assert.True(t, strings.HasPrefix(got[0], env.Providers.Config.BaseURL+"/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/"), got[0])
}

func TestM3UStarredAndUnread(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	starred := podcastEpisode(t, store, "Starred")
	read := podcastEpisode(t, store, "Read")
	podcastEpisode(t, store, "Other")
	require.NoError(t, store.MarkStarred(starred, true))
	require.NoError(t, store.MarkRead(read, true))

	body := env.Get(t, m3uURL(t, env, "alice", "starred/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 1, strings.Count(body, "#EXTINF:"), body)
	assert.True(t, strings.Contains(body, ",Pod – Starred\n"), body)

	body = env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 2, strings.Count(body, "#EXTINF:"), body)
	assert.False(t, strings.Contains(body, ",Pod – Read\n"), body)
}

func TestM3ULimit(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://podcast.example.com/" + strconv.Itoa(nextFeed()), Title: "Pod"})
	require.NoError(t, err)
	for i := range 501 {
		_, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: strconv.Itoa(i), Type: "audio", Title: "Ep",
			MediaURL: "https://cdn.example.com/" + strconv.Itoa(i) + ".mp3"})
		require.NoError(t, err)
	}
	body := env.Get(t, m3uURL(t, env, "alice", "sub/"+strconv.FormatInt(sub, 10)+".m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 500, strings.Count(body, "#EXTINF:"))
}

// An undownloaded YouTube video's entry is its remote master, signed and
// absolute. The M3U resolves nothing: the master does, when mpv asks.
func TestM3UYouTubeNotDownloaded(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	stub := stubTracks(t, env, googlevideo(t))
	id := remoteItem(t, env, "alice")
	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 0, len(stub.asked()))
	got := entryURLs(body)
	require.Equal(t, 1, len(got), body)
	assert.Equal(t, env.Providers.Config.BaseURL+masterURL(t, env, "alice", id), got[0])
	env.Get(t, relative(env, got[0]), cogtest.StopRedirect).Status(http.StatusOK)
}

// A muted item plays only clean audio: one not yet packaged is left out,
// whatever its source; a packaged one plays its clean rendition.
func TestM3UMutedLeftOut(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	stubTracks(t, env, googlevideo(t))
	store := feedStore(t, env, "alice")
	yt := remoteItem(t, env, "alice")
	pod := podcastEpisode(t, store, "Muted")
	podcastEpisode(t, store, "Plain")
	clean := cleanItem(t, env, "alice", true)
	_, err := store.CreateFilter(feed.Filter{SortOrder: 1, Query: "*", Action: "mute_profanity"})
	require.NoError(t, err)
	eng, err := feed.NewFilterEngine(store)
	require.NoError(t, err)
	for _, id := range []int64{yt, pod, clean} {
		require.NoError(t, eng.Apply(id))
	}

	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	got := entryURLs(body)
	require.Equal(t, 2, len(got), body)
	assert.False(t, strings.Contains(body, "Muted"), body)
	assert.False(t, strings.Contains(body, "/apps/feed/hls/alice/"+strconv.FormatInt(yt, 10)+"/"), body)
	assert.False(t, strings.Contains(body, "youtube.com"), body)
	assert.True(t, strings.Contains(body, ",Pod – Plain\n"), body)
	cleanMaster := env.Providers.Config.BaseURL + masterURL(t, env, "alice", clean)
	assert.True(t, strings.Contains(body, "\n"+cleanMaster+"\n"), body)
}

func TestM3UHead(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	pl, _ := mixedPlaylist(t, env)
	u := m3uURL(t, env, "alice", "playlist/"+strconv.FormatInt(pl, 10)+".m3u", false)
	body := env.Get(t, u).Status(http.StatusOK).ReadBody()
	r := env.Head(t, u).Status(http.StatusOK)
	assert.Equal(t, strconv.Itoa(len(body)), r.Header.Get("Content-Length"))
	assert.Equal(t, "", r.ReadBody())
}

// mediaItem stores an undownloaded item of type typ with mediaURL in an RSS
// subscription titled "Pod", and returns its ID.
func mediaItem(t *testing.T, store *feed.Store, typ, title, mediaURL string) int64 {
	t.Helper()
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/" + strconv.Itoa(nextFeed()), Title: "Pod"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "g", Type: typ, Title: title, MediaURL: mediaURL})
	require.NoError(t, err)
	return id
}

// A media URL is feed data, and mpv plays what an M3U names, edl: and
// lavfi: included: only an absolute http(s) URL with a host and no control
// characters becomes an entry.
func TestM3UUnsafeMediaURLLeftOut(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	for _, raw := range []string{
		"file:///etc/passwd",
		"edl://%0%file:///etc/passwd",
		"lavfi://sine",
		"ytdl://x",
		"javascript:alert(1)",
		"/relative.mp3",
		"cdn.example.com/a.mp3",
		"https:///nohost.mp3",
		"https://cdn.example.com/a.mp3\nfile:///etc/passwd",
		"https://cdn.example.com/a.mp3\r\n#EXTINF:1,x",
		"https://cdn.example.com/a\x7f.mp3",
		"https://cdn.example.com/a\x01.mp3",
		"https://cdn.example.com/a\t.mp3",
	} {
		mediaItem(t, store, "audio", "Hostile", raw)
	}
	mediaItem(t, store, "audio", "Fine", "https://cdn.example.com/fine.mp3")
	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, []string{"https://cdn.example.com/fine.mp3"}, entryURLs(body))
	assert.Equal(t, "#EXTM3U\n#EXTINF:-1,Pod – Fine\nhttps://cdn.example.com/fine.mp3\n", body)
}

// A title is folded to one line, so it cannot add an entry.
func TestM3UTitleNewlineNoInjection(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	mediaItem(t, feedStore(t, env, "alice"), "audio", "Evil\nfile:///etc/passwd\r\n#EXTINF:1,x\rlavfi://sine", "https://cdn.example.com/ep.mp3")
	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, 3, strings.Count(body, "\n"), body)
	assert.Equal(t, []string{"https://cdn.example.com/ep.mp3"}, entryURLs(body))
	assert.Equal(t, 1, strings.Count(body, "\n#EXTINF:"), body)
}

func TestM3UEmptyMediaURLLeftOut(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	mediaItem(t, store, "audio", "Empty", "")
	mediaItem(t, store, "video", "Empty video", "")
	mediaItem(t, store, "audio", "Fine", "https://cdn.example.com/fine.mp3")
	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, []string{"https://cdn.example.com/fine.mp3"}, entryURLs(body))
}

// A video that is not YouTube's plays from its own URL until downloaded.
func TestM3UPodcastVideoNotDownloaded(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	mediaItem(t, feedStore(t, env, "alice"), "video", "Vid", "https://cdn.example.com/v.mp4")
	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.Equal(t, "#EXTM3U\n#EXTINF:-1,Pod – Vid\nhttps://cdn.example.com/v.mp4\n", body)
}

// launcherExec is the Exec line of a launcher body.
func launcherExec(t *testing.T, body string) string {
	t.Helper()
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "Exec=") {
			return l
		}
	}
	t.Fatalf("no Exec line in %q", body)
	return ""
}

// execURL is the URL a launcher's Exec line hands mpv, read back as a
// Desktop Entry reader would: the quotes dropped and %% read as %.
func execURL(t *testing.T, line string) string {
	t.Helper()
	arg, ok := strings.CutPrefix(line, `Exec=mpv --force-window "`)
	require.True(t, ok, line)
	arg, ok = strings.CutSuffix(arg, `"`)
	require.True(t, ok, line)
	// Read back as a Desktop Entry reader does: the string level makes \\
	// one backslash, then inside quotes a backslash makes the next
	// character itself.
	arg = strings.ReplaceAll(arg, `\\`, `\`)
	var b strings.Builder
	for i := 0; i < len(arg); i++ {
		if arg[i] == '\\' && i+1 < len(arg) {
			i++
		}
		b.WriteByte(arg[i])
	}
	return strings.ReplaceAll(b.String(), "%%", "%")
}

func TestM3ULauncher(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	pl, err := store.CreatePlaylist(feed.Playlist{Name: "Morning News"})
	require.NoError(t, err)

	path := "/apps/feed/launchers/playlist/" + strconv.FormatInt(pl, 10) + ".m3u"
	r := env.Get(t, path, alice.Auth).Status(http.StatusOK)
	assert.Equal(t, "application/x-desktop", r.Header.Get("Content-Type"))
	assert.Equal(t, `attachment; filename="cog-morning-news.desktop"`, r.Header.Get("Content-Disposition"))
	body := r.ReadBody()
	assert.True(t, strings.HasPrefix(body, "[Desktop Entry]\nType=Application\nName=Cog: Morning News\n"), body)
	assert.True(t, strings.Contains(body, "Comment=Play Morning News from Cog in mpv, silence skipped\n"), body)
	assert.True(t, strings.HasSuffix(body, "Terminal=false\nCategories=AudioVideo;Player;\n"), body)

	m3u := execURL(t, launcherExec(t, body))
	assert.True(t, strings.HasPrefix(m3u, env.Providers.Config.BaseURL+"/apps/feed/m3u/alice/playlist/"), m3u)
	assert.True(t, strings.HasSuffix(m3u, "&skip=silence"), m3u)
	env.Get(t, relative(env, m3u)).Status(http.StatusOK)

	env.Get(t, path).Status(http.StatusUnauthorized)
	env.Get(t, "/apps/feed/launchers/playlist/9999.m3u", alice.Auth).Status(http.StatusNotFound)
	env.Get(t, "/apps/feed/launchers/playlist/x.m3u", alice.Auth).Status(http.StatusBadRequest)
	env.Get(t, "/apps/feed/launchers/starred/7.m3u", alice.Auth).Status(http.StatusBadRequest)
}

// The launcher link a view page really contains downloads a launcher.
func TestM3ULauncherFromPages(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/l", Title: "Pod: Cast"})
	require.NoError(t, err)
	folder, err := store.CreateFolder("Folder")
	require.NoError(t, err)
	pl, err := store.CreatePlaylist(feed.Playlist{Name: "Everything"})
	require.NoError(t, err)
	id := strconv.FormatInt
	for _, tc := range []struct{ page, name string }{
		{"/apps/feed/playlists/" + id(pl, 10), "Everything"},
		{"/apps/feed/folder/" + id(folder, 10), "Folder"},
		{"/apps/feed/subscription/" + id(sub, 10), "Pod: Cast"},
		{"/apps/feed/tag/" + url.PathEscape("a b"), "a b"},
		{"/apps/feed/starred", "Starred"},
		{"/apps/feed/", "Unread"},
	} {
		t.Run(tc.page, func(t *testing.T) {
			page := env.Get(t, tc.page, alice.Auth).Status(http.StatusOK).ReadBody()
			m := regexp.MustCompile(`href="(/apps/feed/launchers/[^"]*)"`).FindStringSubmatch(page)
			require.NotNil(t, m, "no launcher link")
			r := env.Get(t, html.UnescapeString(m[1]), alice.Auth).Status(http.StatusOK)
			assert.True(t, strings.Contains(r.ReadBody(), "Name=Cog: "+tc.name+"\n"))
		})
	}
}

var dispRE = regexp.MustCompile(`^attachment; filename="cog-[a-z0-9-]+\.desktop"$`)

// A tag name is the user's text, and it ends up in an Exec line and a Name.
func TestM3ULauncherHostileTag(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pw")
	for _, tag := range []string{`a"b`, "a`b", "a$b", `a\b`, "a%b", "a b", "a\nb\nExec=evil", "a\r\nb", "a'b", "a;b&c|d"} {
		t.Run(strconv.Quote(tag), func(t *testing.T) {
			r := env.Get(t, "/apps/feed/launchers/tag/"+url.PathEscape(tag)+".m3u", alice.Auth).Status(http.StatusOK)
			disp := r.Header.Get("Content-Disposition")
			assert.True(t, dispRE.MatchString(disp), disp)
			body := r.ReadBody()
			var keys []string
			for _, l := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
				keys = append(keys, strings.SplitN(l, "=", 2)[0])
			}
			assert.Equal(t, []string{"[Desktop Entry]", "Type", "Name", "Comment", "Exec", "Terminal", "Categories"}, keys)
			// The URL the Exec line carries is a signed link that works.
			env.Get(t, relative(env, execURL(t, launcherExec(t, body)))).Status(http.StatusOK)
		})
	}
}
