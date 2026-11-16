package feed_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// waitsOnClock makes AwaitAvailable's wait advance env's clock instead of
// sleeping, and returns what reports each wait so far.
func waitsOnClock(t *testing.T, env *cogtest.Env) func() []time.Duration {
	t.Helper()
	var mu sync.Mutex
	var waits []time.Duration
	t.Cleanup(feed.SetAvailableSleep(func(ctx context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		env.Clock.Advance(d)
		return nil
	}))
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), waits...)
	}
}

// availableLater sets up googlevideo and the resolver so every URL becomes
// available the given time after it is resolved, and is refused before.
func availableLater(t *testing.T, env *cogtest.Env, after time.Duration) (*fakeGooglevideo, *trackStub, func() []time.Duration) {
	t.Helper()
	srv := googlevideo(t)
	stub := stubTracks(t, env, srv)
	stub.availableAfter(after)
	srv.refuseEarly(env, stub)
	return srv, stub, waitsOnClock(t, env)
}

// googlevideo refuses a URL asked for before its available_at, so Cog waits
// for it before reading the indexes, and resolves only once. A cached URL
// past its available_at is not waited for again.
func TestHLSRemoteWaitsForAvailableAt(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	srv, stub, waits := availableLater(t, env, 5*time.Second)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))

	env.Get(t, master).Status(http.StatusOK)
	assert.Equal(t, []time.Duration{5 * time.Second}, waits())
	assert.Equal(t, int32(0), srv.refused.Load())
	assert.Equal(t, 1, len(stub.asked()))

	env.Get(t, sibling(master, "video.m3u8")).Status(http.StatusOK)
	assert.Equal(t, 1, len(waits()))
}

// In direct mode the player is never sent to a URL before its available_at:
// Cog waits, then redirects.
func TestHLSRemoteDirectWaitsBeforeRedirect(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeDirect)
	srv, _, waits := availableLater(t, env, 5*time.Second)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))

	r := env.Get(t, sibling(master, "yt-video-137.mp4"), cogtest.StopRedirect).Status(http.StatusFound)
	assert.Equal(t, []time.Duration{5 * time.Second}, waits())
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, r.Header.Get("Location"), nil)
	require.NoError(t, err)
	req.Header.Set("Range", "bytes=0-99")
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusPartialContent, resp.StatusCode)
}

func TestHLSRemoteProxyWaitsForAvailableAt(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeProxy)
	srv, stub, waits := availableLater(t, env, 5*time.Second)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))

	env.Get(t, sibling(master, "yt-audio-140.m4a"), cogtest.Header("Range", "bytes=0-99")).Status(http.StatusPartialContent)
	assert.Equal(t, []time.Duration{5 * time.Second}, waits())
	assert.Equal(t, int32(0), srv.refused.Load())
	assert.Equal(t, 1, len(stub.asked()))
}

// A URL that becomes available only far ahead is googlevideo failing: Cog
// neither waits that long nor asks for it, nor sends a player to it.
func TestHLSRemoteAvailableTooFar(t *testing.T) {
	env := cogtest.New(t, feed.New())
	env.CreateUser(t, "alice", "pw")
	setYouTubeMode(t, env, feed.YouTubeDirect)
	srv, _, waits := availableLater(t, env, time.Minute)
	master := masterURL(t, env, "alice", remoteItem(t, env, "alice"))

	for _, f := range []string{"master.m3u8", "video.m3u8", "yt-video-137.mp4"} {
		env.Get(t, sibling(master, f), cogtest.StopRedirect).Status(http.StatusBadGateway)
	}
	assert.Equal(t, 0, len(waits()))
	assert.Equal(t, int32(0), srv.hits.Load())
}

// ResolveMedia waits for the URLs too, rather than finding them refused and
// downloading the video.
func TestResolveMediaRemoteWaitsForAvailableAt(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	_, _, waits := availableLater(t, env, 5*time.Second)
	id := remoteItem(t, env, "alice")

	src, err := resolveVideo(t, env, u, id)
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.apple.mpegurl", src.Mime)
	assert.Equal(t, []time.Duration{5 * time.Second}, waits())
	d, err := feedStore(t, env, "alice").FindDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.State)
}
