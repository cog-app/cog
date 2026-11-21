package feed_test

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

// noStream fails the test if a stream is resolved: a muted item's original
// audio is never clean.
func noStream(t *testing.T) {
	t.Helper()
	t.Cleanup(feed.SetStreamResolver(func(context.Context, string, bool) (string, error) {
		t.Error("resolved a stream for a muted item")
		return "https://rr1.googlevideo.com/videoplayback?id=unclean", nil
	}))
}

// requireQueued fails the test unless the item's download is queued.
func requireQueued(t *testing.T, store *feed.Store, id int64) {
	t.Helper()
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)
}

// A muted YouTube item Cog has not downloaded never plays googlevideo's
// audio: asking for it queues the download and answers Unavailable with why,
// without resolving a stream or its tracks.
func TestResolveMediaMutedAudioUnavailable(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	tracks := stubTracks(t, env, googlevideo(t))
	noStream(t)
	id := remoteItem(t, env, "alice")
	store := feedStore(t, env, "alice")
	muteAll(t, store, id)

	_, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_AUDIO}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "Downloading the audio"), err.Error())
	assert.Equal(t, 0, len(tracks.asked()))
	requireQueued(t, store, id)
}

// A muted podcast episode Cog has not downloaded never plays its enclosure.
func TestResolveMediaMutedPodcastUnavailable(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	noStream(t)
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	muteAll(t, store, id)

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	if err == nil {
		t.Fatalf("answered %q for a muted episode", res.Msg.Url)
	}
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "Downloading the audio"), err.Error())
	requireQueued(t, store, id)
}

// The web player's audio of a muted YouTube item Cog has not downloaded
// waits for the download, as ResolveMedia's does.
func TestStreamURLMutedAudioDownloads(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	noStream(t)
	id := remoteItem(t, env, "alice")
	store := feedStore(t, env, "alice")
	muteAll(t, store, id)

	var got map[string]any
	env.Get(t, "/apps/feed/api/stream-url/"+strconv.FormatInt(id, 10)+"?variant=audio", u.Auth).Status(http.StatusOK).JSON(&got)
	assert.Equal(t, true, got["downloading"])
	assert.Equal(t, "Downloading the audio", got["message"])
	assert.Equal(t, nil, got["url"])
	requireQueued(t, store, id)
}

// The same for a muted podcast episode.
func TestStreamURLMutedPodcastDownloads(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	noStream(t)
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	muteAll(t, store, id)

	got := streamURL(t, env, u, id)
	assert.Equal(t, true, got["downloading"])
	assert.Equal(t, nil, got["url"])
	requireQueued(t, store, id)
}

// The web player's audio of a cleaned item is its clean audio, never a
// stream of the original.
func TestStreamURLCleanedAudio(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pw")
	noStream(t)
	id := cleanItem(t, env, "alice", true)

	var got map[string]any
	env.Get(t, "/apps/feed/api/stream-url/"+strconv.FormatInt(id, 10)+"?variant=audio", u.Auth).Status(http.StatusOK).JSON(&got)
	assert.Equal[any](t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", got["url"])
}

// The media route of a muted episode Cog has not downloaded refuses, as it
// does when a clean file is missing, instead of redirecting to the
// enclosure.
func TestItemMediaMutedNotDownloaded(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	muteAll(t, store, id)

	env.Get(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", u.Auth).Status(http.StatusServiceUnavailable)
}
