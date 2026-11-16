package feed_test

import (
	"bytes"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

var feeds atomic.Int64

// nextFeed numbers subscriptions so each is a different feed.
func nextFeed() int { return int(feeds.Add(1)) }

func feedPlugin(t *testing.T, env *cogtest.Env) *feed.Plugin {
	t.Helper()
	raw, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok, "feed plugin not registered")
	return raw.(*feed.Plugin)
}

// packagedItem stores the packaged fixtures as a downloaded item's tracks,
// with a video track when withVideo, and returns its ID.
func packagedItem(t *testing.T, env *cogtest.Env, username string, withVideo bool) int64 {
	t.Helper()
	video := ""
	if withVideo {
		video = "video.mp4"
	}
	return packagedItemFrom(t, env, username, "audio.m4a", video)
}

// packagedItemFrom is packagedItem with the fixtures named: videoFixture is
// empty for an audio-only item.
func packagedItemFrom(t *testing.T, env *cogtest.Env, username, audioFixture, videoFixture string) int64 {
	t.Helper()
	p := feedPlugin(t, env)
	store := feedStore(t, env, username)
	space, err := p.Space(username)
	require.NoError(t, err)
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://podcast.example.com/" + strconv.Itoa(nextFeed())})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "ep", Type: "video", Title: "Episode", MediaURL: "https://cdn.example.com/ep.mp4"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	put := func(fixture, name string) int64 {
		b, err := os.ReadFile("testdata/media/" + fixture)
		require.NoError(t, err)
		n, err := space.Own.Put(name, bytes.NewReader(b))
		require.NoError(t, err)
		return n
	}
	audio := "media/" + strconv.FormatInt(id, 10) + ".m4a"
	size := put(audioFixture, audio)
	video := ""
	if videoFixture != "" {
		video = "media/" + strconv.FormatInt(id, 10) + ".video.mp4"
		size += put(videoFixture, video)
	}
	require.NoError(t, store.MarkVideoDownloadReady(id, audio, video, size))
	require.NoError(t, store.SavePackage(id, audio, video, size))
	return id
}

func masterURL(t *testing.T, env *cogtest.Env, username string, id int64) string {
	t.Helper()
	u, err := feedPlugin(t, env).HLSMasterURL(feedStore(t, env, username), username, id, feed.HLSOptions{})
	require.NoError(t, err)
	return u
}

// sibling is the URL of another file of the same presentation.
func sibling(master, file string) string {
	return strings.Replace(master, "/master.m3u8?", "/"+file+"?", 1)
}

func TestHLSMasterNoCookie(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	u := masterURL(t, env, "alice", id)
	assert.True(t, strings.HasPrefix(u, "/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/master.m3u8?sig="), u)

	r := env.Get(t, u, cogtest.StopRedirect).Status(http.StatusOK)
	assert.Equal(t, "application/vnd.apple.mpegurl", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
	assert.Equal(t, "", r.Header.Get("Set-Cookie"))
	body := r.ReadBody()
	for _, want := range []string{"#EXT-X-STREAM-INF", `URI="audio.m3u8?sig=`, "\nvideo.m3u8?sig="} {
		assert.True(t, strings.Contains(body, want), "%q not in\n%s", want, body)
	}
}

func TestHLSMediaAndRanges(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	master := masterURL(t, env, "alice", id)
	fixture, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)

	pl := env.Get(t, sibling(master, "video.m3u8")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(pl, `#EXT-X-MAP:URI="video.mp4?sig=`), pl)
	ranges := regexp.MustCompile(`#EXT-X-BYTERANGE:(\d+)@(\d+)`).FindAllStringSubmatch(pl, -1)
	require.True(t, len(ranges) > 0, pl)
	for _, m := range ranges {
		n, _ := strconv.Atoi(m[1])
		off, _ := strconv.Atoi(m[2])
		assert.True(t, off+n <= len(fixture), "range %s@%s past %d", m[1], m[2], len(fixture))
	}
	n, _ := strconv.Atoi(ranges[0][1])
	off, _ := strconv.Atoi(ranges[0][2])
	r := env.Get(t, sibling(master, "video.mp4"), cogtest.Header("Range", "bytes="+strconv.Itoa(off)+"-"+strconv.Itoa(off+n-1))).
		Status(http.StatusPartialContent)
	assert.Equal(t, "video/mp4", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", r.Header.Get("Cache-Control"))
	assert.True(t, r.ReadBody() == string(fixture[off:off+n]), "segment bytes differ")

	env.Get(t, sibling(master, "audio.m4a"), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
	env.Get(t, sibling(master, "audio.m3u8")).Status(http.StatusOK).Contains("#EXT-X-ENDLIST")
	h := env.Head(t, master).Status(http.StatusOK)
	assert.Equal(t, "application/vnd.apple.mpegurl", h.Header.Get("Content-Type"))
}

func TestHLSAudioOnlyItem(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", false)
	master := masterURL(t, env, "alice", id)
	body := env.Get(t, master).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "#EXT-X-MEDIA:"), body)
	assert.Equal(t, 1, strings.Count(body, "#EXT-X-STREAM-INF:"), body)
	env.Get(t, sibling(master, "video.m3u8")).Status(http.StatusNotFound)
	env.Get(t, sibling(master, "video.mp4")).Status(http.StatusNotFound)
}

func TestHLSRejects(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	env.CreateUser(t, "bob", "pw")
	alice := packagedItem(t, env, "alice", true)
	other := packagedItem(t, env, "alice", false) // a second item of alice's
	bob := packagedItem(t, env, "bob", true)
	master := masterURL(t, env, "alice", alice)
	sig := master[strings.Index(master, "?sig=")+5:]
	a, o, b := strconv.FormatInt(alice, 10), strconv.FormatInt(other, 10), strconv.FormatInt(bob, 10)

	for _, c := range []struct {
		name, path string
		want       int
	}{
		{"bad sig", "/apps/feed/hls/alice/" + a + "/master.m3u8?sig=AAAA", http.StatusForbidden},
		{"no sig", "/apps/feed/hls/alice/" + a + "/master.m3u8", http.StatusForbidden},
		{"another item's sig", "/apps/feed/hls/alice/" + o + "/master.m3u8?sig=" + sig, http.StatusForbidden},
		{"alice's sig on bob's path", "/apps/feed/hls/bob/" + b + "/master.m3u8?sig=" + sig, http.StatusForbidden},
		{"extra parameter", master + "&mute=profanity", http.StatusBadRequest},
		{"unknown file", sibling(master, "secrets.txt"), http.StatusNotFound},
		{"unknown user", "/apps/feed/hls/mallory/" + a + "/master.m3u8?sig=" + sig, http.StatusForbidden},
		{"bad item", "/apps/feed/hls/alice/x/master.m3u8?sig=" + sig, http.StatusNotFound},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := env.Get(t, c.path, cogtest.StopRedirect).Status(c.want)
			assert.Equal(t, "", r.Header.Get("Set-Cookie"))
			assert.Equal(t, "", r.Header.Get("Location"))
		})
	}
}

func TestHLSSkipNeedsItsOwnSignature(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	plain := masterURL(t, env, "alice", id)
	bad := env.Get(t, plain+"&skip=silence", cogtest.StopRedirect).Status(http.StatusForbidden).ReadBody()
	wrong := env.Get(t, strings.Replace(plain, "sig=", "sig=AAAA", 1), cogtest.StopRedirect).Status(http.StatusForbidden).ReadBody()
	assert.Equal(t, wrong, bad)

	skip, err := feedPlugin(t, env).HLSSkipMasterURL(feedStore(t, env, "alice"), "alice", id)
	require.NoError(t, err)
	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, "&skip=silence"), body)
	// Every URI carries the option.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, ".m3u8") {
			assert.True(t, strings.Contains(line, "&skip=silence"), line)
		}
	}
	// The plain signature on a skip link is refused, and skip alone is too.
	env.Get(t, sibling(skip, "audio.m3u8")).Status(http.StatusOK)
	env.Get(t, strings.Replace(skip, "&skip=silence", "", 1)).Status(http.StatusForbidden)
	// An edit list is accepted with skip, outside the signature.
	env.Get(t, skip+"&edit=abcdef012345").Status(http.StatusOK)
	env.Get(t, plain+"&edit=abcdef012345").Status(http.StatusBadRequest)
}

func TestHLSLinkDoesNotExpire(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	master := masterURL(t, env, "alice", id)
	env.Clock.Advance(10 * 365 * 24 * time.Hour)
	env.Get(t, master).Status(http.StatusOK)
}

func TestHLSRevoke(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	old := masterURL(t, env, "alice", id)
	env.Get(t, old).Status(http.StatusOK)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	session := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	r := env.PostForm(t, "/apps/feed/settings/media-links/revoke", url.Values{"csrf_token": {csrf}},
		session, cogtest.StopRedirect).Status(http.StatusSeeOther)
	assert.Equal(t, "/apps/feed/settings/sync", r.Header.Get("Location"))
	assert.True(t, strings.Contains(r.Header.Get("Set-Cookie"), "flash"), r.Header.Get("Set-Cookie"))

	env.Get(t, old).Status(http.StatusForbidden)
	env.Get(t, masterURL(t, env, "alice", id)).Status(http.StatusOK)
}

func TestHLSNotPackaged(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep.mp3")
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", 1))
	env.Get(t, masterURL(t, env, "alice", id)).Status(http.StatusNotFound)
}

// Serving a link only reads the key: a request for a user who never signed
// one finds none, is refused, and writes nothing.
func TestHLSNoKeyWritesNothing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	env.Get(t, "/apps/feed/hls/alice/1/master.m3u8?sig=AAAA", cogtest.StopRedirect).Status(http.StatusForbidden)
	var n int
	require.NoError(t, feedStore(t, env, "alice").DB().QueryRow(`SELECT count(*) FROM feed_settings WHERE key = 'hls_key'`).Scan(&n))
	assert.Equal(t, 0, n)
}

const testVTT = "WEBVTT\n\n00:00:03.000 --> 00:00:05.000\nHello there\n\n00:00:08.000 --> 00:00:09.000\nLater\n"

// captionedItem is packagedItem with captions: vtt is stored as the item's
// captions file.
func captionedItem(t *testing.T, env *cogtest.Env, username string, withVideo bool, vtt string) int64 {
	t.Helper()
	video := ""
	if withVideo {
		video = "video.mp4"
	}
	return captionedItemFrom(t, env, username, "audio.m4a", video, vtt)
}

// captionedItemFrom is captionedItem with the fixtures named, as
// packagedItemFrom does.
func captionedItemFrom(t *testing.T, env *cogtest.Env, username, audioFixture, videoFixture, vtt string) int64 {
	t.Helper()
	id := packagedItemFrom(t, env, username, audioFixture, videoFixture)
	space, err := feedPlugin(t, env).Space(username)
	require.NoError(t, err)
	name := "captions/" + strconv.FormatInt(id, 10) + ".vtt"
	_, err = space.Own.Put(name, strings.NewReader(vtt))
	require.NoError(t, err)
	require.NoError(t, feedStore(t, env, username).SetCaptionsPath(id, name))
	return id
}

func TestHLSSubtitlesInMaster(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	plain := packagedItem(t, env, "alice", true)
	captioned := captionedItem(t, env, "alice", true, testVTT)
	body := env.Get(t, masterURL(t, env, "alice", plain)).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "TYPE=SUBTITLES"), body)
	body = env.Get(t, masterURL(t, env, "alice", captioned)).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `TYPE=SUBTITLES,GROUP-ID="subs",NAME="Captions",DEFAULT=NO,AUTOSELECT=YES,URI="subs.m3u8?sig=`), body)
	assert.Equal(t, 2, strings.Count(body, `SUBTITLES="subs"`), body)
}

func TestHLSSubsPlaylistAndSegments(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItem(t, env, "alice", false, testVTT)
	m := masterURL(t, env, "alice", id)
	pl := env.Get(t, sibling(m, "subs.m3u8")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(pl, "\nsubs-0.vtt?sig="), pl)
	assert.False(t, strings.Contains(pl, "#EXT-X-MAP"), pl)
	r := env.Get(t, sibling(m, "subs-0.vtt")).Status(http.StatusOK)
	assert.Equal(t, "text/vtt; charset=utf-8", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
	seg := r.ReadBody()
	assert.True(t, strings.Contains(seg, "X-TIMESTAMP-MAP=MPEGTS:"), seg)
	assert.True(t, strings.Contains(seg, "00:00:03.000 --> 00:00:05.000\nHello there\n"), seg)
	// The 12 s audio fixture has 2 s fragments: segments 0–6 s and 6–12 s.
	seg1 := env.Get(t, sibling(m, "subs-1.vtt")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(seg1, "Later"), seg1)
	assert.False(t, strings.Contains(seg1, "Hello there"), seg1)
}

func TestHLSSubsBadSegment(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItem(t, env, "alice", false, testVTT)
	m := masterURL(t, env, "alice", id)
	for _, f := range []string{"subs-99.vtt", "subs--1.vtt", "subs-x.vtt", "subs-.vtt"} {
		env.Get(t, sibling(m, f)).Status(http.StatusNotFound)
	}
	env.Get(t, sibling(masterURL(t, env, "alice", packagedItem(t, env, "alice", false)), "subs.m3u8")).Status(http.StatusNotFound)
}

func TestHLSSubsNeedSignature(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItem(t, env, "alice", false, testVTT)
	bad := regexp.MustCompile(`sig=[^&]+`).ReplaceAllString(sibling(masterURL(t, env, "alice", id), "subs-0.vtt"), "sig=AAAA")
	env.Get(t, bad).Status(http.StatusForbidden)
}

func TestHLSCaptionsFileMissing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := captionedItem(t, env, "alice", true, testVTT)
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	require.NoError(t, space.Own.Remove("captions/"+strconv.FormatInt(id, 10)+".vtt"))
	body := env.Get(t, masterURL(t, env, "alice", id)).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "TYPE=SUBTITLES"), body)
	assert.True(t, strings.Contains(body, "\nvideo.m3u8?sig="), body)
}

// cleanItem is packagedItem plus a clean rendition: the audio fixture stored
// as the item's packaged clean track.
func cleanItem(t *testing.T, env *cogtest.Env, username string, withVideo bool) int64 {
	t.Helper()
	id := packagedItem(t, env, username, withVideo)
	space, err := feedPlugin(t, env).Space(username)
	require.NoError(t, err)
	b, err := os.ReadFile("testdata/media/audio.m4a")
	require.NoError(t, err)
	name := "media/" + strconv.FormatInt(id, 10) + ".hls-audio.clean.m4a"
	_, err = space.Own.Put(name, bytes.NewReader(b))
	require.NoError(t, err)
	require.NoError(t, feedStore(t, env, username).SaveCleanTrack(id, name))
	return id
}

func TestHLSCleanRendition(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", true)
	body := env.Get(t, masterURL(t, env, "alice", id)).Status(http.StatusOK).ReadBody()
	for _, want := range []string{`NAME="Clean",DEFAULT=YES`, `URI="clean.m3u8?sig=`, `NAME="Original",DEFAULT=NO`, `URI="audio.m3u8?sig=`} {
		assert.True(t, strings.Contains(body, want), "%q not in\n%s", want, body)
	}
}

func TestHLSCleanMediaAndRange(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", false)
	master := masterURL(t, env, "alice", id)
	pl := env.Get(t, sibling(master, "clean.m3u8")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(pl, "clean.m4a?sig="), pl)
	assert.True(t, strings.Contains(pl, `#EXT-X-MAP:URI="clean.m4a?sig=`), pl)
	r := env.Get(t, sibling(master, "clean.m4a"), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
	assert.Equal(t, "audio/mp4", r.Header.Get("Content-Type"))
	assert.Equal(t, 100, len(r.ReadBody()))
}

func TestHLSNoCleanTrack(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	master := masterURL(t, env, "alice", id)
	env.Get(t, sibling(master, "clean.m3u8")).Status(http.StatusNotFound)
	env.Get(t, sibling(master, "clean.m4a")).Status(http.StatusNotFound)
	body := env.Get(t, master).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, `NAME="Clean"`), body)
}

func TestHLSCaptionsMaskedWhenClean(t *testing.T) {
	env := cogtest.New(t, feed.New(), profanity.New())
	// Start is where the feed plugin finds the profanity plugin.
	require.NoError(t, env.Providers.Plugins.Start(t.Context()))
	u := env.CreateUser(t, "alice", "pw")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	env.PostForm(t, "/apps/profanity/settings", url.Values{"wordlist": {"darn"}},
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID),
		cogtest.Header("X-CSRF-Token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)),
		cogtest.StopRedirect).Status(http.StatusSeeOther)
	raw, ok := env.Providers.Plugins.Plugin(profanity.PluginID)
	require.True(t, ok)
	masked := raw.(*profanity.Plugin).FilterText("alice", "well darn it")
	require.NotEqual(t, "well darn it", masked)

	const vtt = "WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nwell darn it\n"
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	caption := func(id int64) {
		name := "captions/" + strconv.FormatInt(id, 10) + ".vtt"
		_, err := space.Own.Put(name, strings.NewReader(vtt))
		require.NoError(t, err)
		require.NoError(t, feedStore(t, env, "alice").SetCaptionsPath(id, name))
	}
	clean := cleanItem(t, env, "alice", false)
	caption(clean)
	seg := env.Get(t, sibling(masterURL(t, env, "alice", clean), "subs-0.vtt")).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(seg, "darn"), seg)
	assert.True(t, strings.Contains(seg, masked), "%q not in\n%s", masked, seg)

	plain := packagedItem(t, env, "alice", false)
	caption(plain)
	seg = env.Get(t, sibling(masterURL(t, env, "alice", plain), "subs-0.vtt")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(seg, "darn"), seg)
}

// With a clean track and no profanity plugin there is no word list to mask
// with, so the captions are withheld rather than served unmasked.
func TestHLSCaptionsWithheldWithoutProfanity(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	id := cleanItem(t, env, "alice", false)
	space, err := feedPlugin(t, env).Space("alice")
	require.NoError(t, err)
	name := "captions/" + strconv.FormatInt(id, 10) + ".vtt"
	_, err = space.Own.Put(name, strings.NewReader("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\nwell darn it\n"))
	require.NoError(t, err)
	require.NoError(t, feedStore(t, env, "alice").SetCaptionsPath(id, name))
	m := masterURL(t, env, "alice", id)
	body := env.Get(t, m).Status(http.StatusOK).ReadBody()
	assert.False(t, strings.Contains(body, "SUBTITLES"), body)
	env.Get(t, sibling(m, "subs.m3u8")).Status(http.StatusNotFound)
	env.Get(t, sibling(m, "subs-0.vtt")).Status(http.StatusNotFound)
}
