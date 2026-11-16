package feed_test

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

func TestHLSRemoteMaster(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	id := remoteItem(t, env, "alice")
	master := masterURL(t, env, "alice", id)

	r := env.Get(t, master, cogtest.StopRedirect).Status(http.StatusOK)
	assert.Equal(t, "application/vnd.apple.mpegurl", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, no-cache", r.Header.Get("Cache-Control"))
	assert.Equal(t, "", r.Header.Get("Set-Cookie"))
	body := r.ReadBody()
	for _, want := range []string{"video.m3u8?sig=", `URI="audio.m3u8?sig=`, "RESOLUTION=160x90"} {
		assert.True(t, strings.Contains(body, want), "%q not in\n%s", want, body)
	}
	for _, not := range []string{"SUBTITLES", `NAME="Clean"`, "SESSION-DATA"} {
		assert.False(t, strings.Contains(body, not), "%q in\n%s", not, body)
	}

	pl := env.Get(t, sibling(master, "video.m3u8")).Status(http.StatusOK)
	assert.Equal(t, "private, no-cache", pl.Header.Get("Cache-Control"))
	vp := pl.ReadBody()
	for _, want := range []string{"#EXT-X-BYTERANGE:", "yt-video.mp4?sig=", `#EXT-X-MAP:URI="yt-video.mp4?sig=`} {
		assert.True(t, strings.Contains(vp, want), "%q not in\n%s", want, vp)
	}
	ap := env.Get(t, sibling(master, "audio.m3u8")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(ap, "yt-audio.m4a?sig="), ap)
	// The local presentation's files are not the remote one's.
	for _, f := range []string{"audio.m4a", "video.mp4", "clean.m3u8", "subs.m3u8", "subs-0.vtt"} {
		env.Get(t, sibling(master, f)).Status(http.StatusNotFound)
	}
	// One resolve, cached for every request after it.
	assert.Equal(t, 1, len(stub.asked()))
}

func TestHLSRemoteDirectRedirects(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeDirect)
	srv := googlevideo(t)
	stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))

	r := env.Get(t, sibling(master, "yt-video.mp4"), cogtest.Header("Range", "bytes=0-99"), cogtest.StopRedirect).
		Status(http.StatusFound)
	loc := r.Header.Get("Location")
	assert.Equal(t, srv.URL+"/video", loc)
	assert.Equal(t, "no-store", r.Header.Get("Cache-Control"))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, loc, nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)

	r = env.Get(t, sibling(master, "yt-audio.m4a"), cogtest.StopRedirect).Status(http.StatusFound)
	assert.Equal(t, srv.URL+"/audio", r.Header.Get("Location"))
}

func TestHLSRemoteProxyRange(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv := googlevideo(t)
	stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	data, err := os.ReadFile("testdata/media/video.mp4")
	require.NoError(t, err)
	size := strconv.Itoa(len(data))

	r := env.Get(t, sibling(master, "yt-video.mp4"), cogtest.Header("Range", "bytes=10-109"), cogtest.StopRedirect).
		Status(http.StatusPartialContent)
	assert.Equal(t, "bytes 10-109/"+size, r.Header.Get("Content-Range"))
	assert.Equal(t, "100", r.Header.Get("Content-Length"))
	assert.Equal(t, "video/mp4", r.Header.Get("Content-Type"))
	assert.Equal(t, "private, max-age=86400", r.Header.Get("Cache-Control"))
	assert.True(t, r.ReadBody() == string(data[10:110]), "proxied bytes differ")

	h := env.Head(t, sibling(master, "yt-video.mp4")).Status(http.StatusOK)
	assert.Equal(t, size, h.Header.Get("Content-Length"))
	assert.Equal(t, "bytes", h.Header.Get("Accept-Ranges"))

	env.Get(t, sibling(master, "yt-video.mp4"), cogtest.Header("Range", "bytes="+size+"-")).
		Status(http.StatusRequestedRangeNotSatisfiable)
}

// The proxy sends googlevideo only the range it was asked: none of the
// client's credentials, and none of googlevideo's cookies come back.
func TestHLSRemoteProxyHeaders(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv := googlevideo(t)
	stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	const ifRange = `"v1"`

	r := env.Get(t, sibling(master, "yt-video.mp4"),
		cogtest.Header("Range", "bytes=0-99"),
		cogtest.Header("If-Range", ifRange),
		cogtest.Header("Cookie", "cog_session=secret"),
		cogtest.BasicAuth("alice", "pw"),
		cogtest.StopRedirect)
	// googlevideo's fixture has no ETag, so If-Range does not match and the
	// whole file comes back.
	r.Status(http.StatusOK)
	assert.Equal(t, "", r.Header.Get("Set-Cookie"))
	up := srv.lastHeader()
	assert.Equal(t, "bytes=0-99", up.Get("Range"))
	assert.Equal(t, ifRange, up.Get("If-Range"))
	assert.Equal(t, "", up.Get("Cookie"))
	assert.Equal(t, "", up.Get("Authorization"))
}

// A URL still refused after the one refresh is a bad gateway.
func TestHLSRemoteProxyRefusedTwice(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	srv.refuse.Store(true)
	env.Get(t, sibling(master, "yt-video.mp4"), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusBadGateway)
	assert.Equal(t, 2, len(stub.asked()))
	assert.Equal(t, int32(2), srv.hits.Load())
}

// A failing store is the server's fault, not googlevideo's.
func TestHLSRemoteStoreFailure(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	_, err := feedStore(t, env, "alice").DB().Exec(`DROP TABLE youtube_streams`)
	require.NoError(t, err)
	for _, f := range []string{"master.m3u8", "video.m3u8", "yt-video.mp4"} {
		env.Get(t, sibling(master, f), cogtest.StopRedirect).Status(http.StatusInternalServerError)
	}
	assert.Equal(t, 0, len(stub.asked()))
}

func TestHLSRemoteProxyRefreshesOn403(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	env.Get(t, master).Status(http.StatusOK)

	srv.refuse.Store(true)
	stub.set(srv.URL+"/video2", srv.URL+"/audio2", nil)
	before := srv.hits.Load()
	env.Get(t, sibling(master, "yt-video.mp4"), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
	// The refused URL, then the fresh one.
	assert.Equal(t, int32(2), srv.hits.Load()-before)
	asked := stub.asked()
	require.Equal(t, 2, len(asked))
	assert.Equal(t, "(137)+(140)", asked[1])
}

// Any other failure upstream is a bad gateway, not the upstream's answer.
func TestHLSRemoteProxyUpstreamError(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv := googlevideo(t)
	stubTracks(t, env, srv).set(srv.URL+"/missing", srv.URL+"/missing", nil)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	env.Get(t, sibling(master, "yt-audio.m4a")).Status(http.StatusBadGateway)
}

func TestHLSRemoteBadSignature(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	bad := strings.Replace(master, "sig=", "sig=AAAA", 1)
	env.Get(t, bad, cogtest.StopRedirect).Status(http.StatusForbidden)
	env.Get(t, sibling(bad, "yt-video.mp4"), cogtest.StopRedirect).Status(http.StatusForbidden)
	assert.Equal(t, 0, len(stub.asked()))
	assert.Equal(t, int32(0), srv.hits.Load())
}

func TestHLSRemoteNotYouTube(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	id := seedAudioItem(t, feedStore(t, env, "alice"), "https://cdn.example.com/ep.mp3")
	master := masterURL(t, env, "alice", id)
	env.Get(t, master).Status(http.StatusNotFound)
	env.Get(t, sibling(master, "yt-audio.m4a"), cogtest.StopRedirect).Status(http.StatusNotFound)
	assert.Equal(t, 0, len(stub.asked()))
}

func TestHLSRemoteResolveFails(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	stub.set("", "", errors.New("yt-dlp: video unavailable"))
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))
	r := env.Get(t, master).Status(http.StatusBadGateway)
	assert.False(t, strings.Contains(r.ReadBody(), "#EXTM3U"))
	env.Get(t, sibling(master, "yt-video.mp4"), cogtest.StopRedirect).Status(http.StatusBadGateway)
}

// A remote presentation has no download, so it has no silences to skip:
// a skip master plays it whole and says so.
func TestHLSRemoteSkipUnavailable(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv := googlevideo(t)
	stubTracks(t, env, srv)
	id := remoteItem(t, env, "alice")
	skip, err := feedPlugin(t, env).HLSSkipMasterURL(feedStore(t, env, "alice"), "alice", id)
	require.NoError(t, err)

	body := env.Get(t, skip).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `#EXT-X-SESSION-DATA:DATA-ID="us.duckfam.cog.skip",VALUE="unavailable"`), body)
	assert.False(t, strings.Contains(body, "us.duckfam.cog.edits"), body)
	for line := range strings.SplitSeq(body, "\n") {
		if strings.Contains(line, ".m3u8") {
			assert.True(t, strings.Contains(line, "skip=silence"), line)
		}
	}
	pl := env.Get(t, sibling(skip, "video.m3u8")).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(pl, "yt-video.mp4?sig="), pl)
	assert.False(t, strings.Contains(pl, "#EXT-X-DISCONTINUITY"), pl)
	env.Get(t, sibling(skip, "video.m3u8")+"&edit=abcdef012345").Status(http.StatusNotFound)
}
