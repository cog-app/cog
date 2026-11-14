package feed_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

func TestResolveMediaEnclosure(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := seedAudioItem(t, feedStore(t, env, u.Username), "https://cdn.example.com/ep1.mp3")

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/ep1.mp3", res.Msg.Url)
	assert.Equal(t, feedpb.Variant_VARIANT_AUDIO, res.Msg.Variant)
	assert.True(t, res.Msg.ExpiresAt == nil)
}

// A downloaded item plays from Cog's media route, which the client fetches
// with its own credentials.
func TestResolveMediaDownloaded(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	space, err := raw.(*feed.Plugin).Space(u.Username)
	require.NoError(t, err)
	n, err := space.Own.Put("media/1.mp3", strings.NewReader("0123456789"))
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkDownloadStart(id))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", n))

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	require.NoError(t, err)
	assert.Equal(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", res.Msg.Url)
	assert.Equal(t, int64(10), res.Msg.Bytes)
}

// A YouTube video Cog has not downloaded is resolved to a stream: audio only
// when asked, expiring when the stream URL says.
func TestResolveMediaYouTube(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://www.youtube.com/channel/UC1", Title: "Talks"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "v1", Type: "video", Title: "Talk", MediaURL: "https://www.youtube.com/watch?v=abc"})
	require.NoError(t, err)
	var gotAudio bool
	restore := feed.SetStreamResolver(func(_ context.Context, watch string, audio bool) (string, error) {
		gotAudio = audio
		return "https://rr1.googlevideo.com/videoplayback?expire=1900000000&id=" + watch[len(watch)-3:], nil
	})
	t.Cleanup(restore)

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_AUDIO}))
	require.NoError(t, err)
	assert.True(t, gotAudio)
	assert.Equal(t, "https://rr1.googlevideo.com/videoplayback?expire=1900000000&id=abc", res.Msg.Url)
	assert.Equal(t, feedpb.Variant_VARIANT_AUDIO, res.Msg.Variant)
	assert.Equal(t, time.Unix(1900000000, 0).UTC(), res.Msg.ExpiresAt.AsTime())
}

func TestResolveMediaNothingToPlay(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := seedAudioItem(t, feedStore(t, env, u.Username), "")

	_, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}
