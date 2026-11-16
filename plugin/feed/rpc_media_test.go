package feed_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

// A YouTube video Cog has not downloaded, and cannot play from googlevideo,
// is downloaded first. Asking queues the download and answers Unavailable
// until it is ready, without resolving a stream.
func TestResolveMediaYouTubeVideoDownloadsWhenRemoteFails(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	stubTracks(t, env, googlevideo(t)).set("", "", errors.New("yt-dlp: sign in to confirm"))
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "youtube", URL: "https://www.youtube.com/channel/UC1", Title: "Talks"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "v1", Type: "video", Title: "Talk", MediaURL: "https://www.youtube.com/watch?v=abc"})
	require.NoError(t, err)
	restore := feed.SetStreamResolver(func(context.Context, string, bool) (string, error) {
		t.Error("resolved a stream for a video")
		return "", nil
	})
	t.Cleanup(restore)
	client := feedClient(t, env, u.AppPassword(t, "phone"))

	_, err = client.ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_VIDEO}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)

	// A download that failed says why, and asking again retries it.
	require.NoError(t, store.MarkDownloadStart(id))
	require.NoError(t, store.MarkDownloadError(id, "yt-dlp: no formats"))
	_, err = client.ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "yt-dlp: no formats"), err.Error())
	d, err = store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)
}

// A packaged video plays from its signed HLS master playlist, which needs
// no credentials and does not expire.
func TestResolveMediaPackagedVideo(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := packagedItem(t, env, "alice", true)

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_VIDEO}))
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.apple.mpegurl", res.Msg.Mime)
	assert.Equal(t, feedpb.Variant_VARIANT_VIDEO, res.Msg.Variant)
	assert.True(t, strings.HasPrefix(res.Msg.Url, "/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/master.m3u8?sig="), res.Msg.Url)
	assert.True(t, res.Msg.ExpiresAt == nil)
	env.Get(t, res.Msg.Url, cogtest.StopRedirect).Status(http.StatusOK)
}

// Its audio alone plays from the media route, which serves the audio track.
func TestResolveMediaPackagedAudio(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := packagedItem(t, env, "alice", true)

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_AUDIO}))
	require.NoError(t, err)
	assert.Equal(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", res.Msg.Url)
	assert.Equal(t, feedpb.Variant_VARIANT_AUDIO, res.Msg.Variant)
}

// A video downloaded as two tracks waits for the packager.
func TestResolveMediaPreparing(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	id := twoTrackDownload(t, store)

	_, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_VIDEO}))
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "Preparing the video"), err.Error())
}

func resolveCleaned(t *testing.T, env *cogtest.Env, u *cogtest.User, id int64) *feedpb.MediaSource {
	t.Helper()
	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	require.NoError(t, err)
	return res.Msg
}

// A cleaned video plays HLS, whose default audio is the clean rendition.
func TestResolveMediaCleanedVideoPlaysHLS(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := cleanItem(t, env, "alice", true)
	src := resolveCleaned(t, env, u, id)
	assert.Equal(t, "application/vnd.apple.mpegurl", src.Mime)
	assert.Equal(t, feedpb.Variant_VARIANT_VIDEO, src.Variant)
	assert.True(t, src.Cleaned)
	assert.True(t, strings.HasPrefix(src.Url, "/apps/feed/hls/"), src.Url)
}

// Clean audio that is not yet a track, or failed to become one, plays as
// audio from the media route: never HLS with the unclean audio.
func TestResolveMediaCleanPendingPlaysCleanAudio(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := packagedItem(t, env, "alice", true)
	store := feedStore(t, env, "alice")
	require.NoError(t, store.SetCleanAudioPath(id, "media/clean.m4a"))
	want := "/apps/feed/items/" + strconv.FormatInt(id, 10) + "/media"
	check := func() {
		t.Helper()
		src := resolveCleaned(t, env, u, id)
		assert.Equal(t, want, src.Url)
		assert.Equal(t, feedpb.Variant_VARIANT_AUDIO, src.Variant)
		assert.True(t, src.Cleaned)
	}
	check()
	require.NoError(t, store.MarkCleanTrackError(id, "bad"))
	check()
}

// An audio item stays progressive, from its cleaned file.
func TestResolveMediaCleanedAudioItem(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://podcast.example.com/clean-audio"})
	require.NoError(t, err)
	id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: "a", Type: "audio", MediaURL: "https://cdn.example.com/a.mp3"})
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkVideoDownloadReady(id, "media/a.m4a", "", 10))
	require.NoError(t, store.SavePackage(id, "media/a.m4a", "", 10))
	require.NoError(t, store.SaveCleanTrack(id, "media/a.clean.m4a"))
	src := resolveCleaned(t, env, u, id)
	assert.Equal(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", src.Url)
	assert.Equal(t, feedpb.Variant_VARIANT_AUDIO, src.Variant)
	assert.True(t, src.Cleaned)
}

// resolveVideo asks ResolveMedia for an item's video.
func resolveVideo(t *testing.T, env *cogtest.Env, u *cogtest.User, id int64) (*feedpb.MediaSource, error) {
	t.Helper()
	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: feedpb.Variant_VARIANT_VIDEO}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// An undownloaded YouTube video plays its googlevideo tracks over HLS,
// without downloading.
func TestResolveMediaRemoteYouTube(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	stubTracks(t, env, googlevideo(t))
	id := remoteItem(t, env, "alice")

	src, err := resolveVideo(t, env, u, id)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(src.Url, "/apps/feed/hls/alice/"), src.Url)
	assert.Equal(t, "application/vnd.apple.mpegurl", src.Mime)
	assert.Equal(t, feedpb.Variant_VARIANT_VIDEO, src.Variant)
	assert.True(t, src.ExpiresAt == nil)
	d, err := feedStore(t, env, "alice").FindDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "", d.State)
}

// Tracks that are not indexed fMP4 cannot be HLS: the video is downloaded.
func TestResolveMediaRemoteUnindexedFallsBack(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	files := httptest.NewServer(http.FileServer(http.Dir("testdata/media")))
	t.Cleanup(files.Close)
	stubTracks(t, env, googlevideo(t)).set(files.URL+"/plain-video.mp4", files.URL+"/plain-audio.m4a", nil)
	id := remoteItem(t, env, "alice")

	_, err := resolveVideo(t, env, u, id)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	d, err := feedStore(t, env, "alice").GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)
}

// A download under way plays remote meanwhile, and is left alone.
func TestResolveMediaRemoteKeepsDownload(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	stubTracks(t, env, googlevideo(t))
	id := remoteItem(t, env, "alice")
	store := feedStore(t, env, "alice")
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkDownloadStart(id))

	src, err := resolveVideo(t, env, u, id)
	require.NoError(t, err)
	assert.Equal(t, "application/vnd.apple.mpegurl", src.Mime)
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "downloading", d.State)
}

// muteAll makes a mute filter match every item, and applies it to id.
func muteAll(t *testing.T, store *feed.Store, id int64) {
	t.Helper()
	_, err := store.CreateFilter(feed.Filter{SortOrder: 1, Query: "*", Action: "mute_profanity"})
	require.NoError(t, err)
	eng, err := feed.NewFilterEngine(store)
	require.NoError(t, err)
	require.NoError(t, eng.Apply(id))
}

// An item a mute filter matches never plays googlevideo's unclean audio: it
// is downloaded, to be cleaned, without resolving its tracks.
func TestResolveMediaRemoteMutedDownloads(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	stub := stubTracks(t, env, googlevideo(t))
	id := remoteItem(t, env, "alice")
	store := feedStore(t, env, "alice")
	muteAll(t, store, id)

	_, err := resolveVideo(t, env, u, id)
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	assert.True(t, strings.Contains(err.Error(), "Downloading the video"), err.Error())
	assert.Equal(t, 0, len(stub.asked()))
	d, err := store.GetDownload(id)
	require.NoError(t, err)
	assert.Equal(t, "queued", d.State)
}
