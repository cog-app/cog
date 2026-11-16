package feed_test

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
	"git.duckfam.us/jj-software/cog/plugin/feed/pipeline"
	"git.duckfam.us/jj-software/cog/plugin/profanity"
)

// cleaningEnv is a server whose feed plugin has found the profanity
// plugin, as it does on Start.
func cleaningEnv(t *testing.T) *cogtest.Env {
	t.Helper()
	env := cogtest.New(t, feed.New(), profanity.New())
	require.NoError(t, env.Providers.Plugins.Start(t.Context()))
	return env
}

// uncleanEpisode is a podcast episode Cog has downloaded that a mute filter
// matched and that is not cleaned yet.
func uncleanEpisode(t *testing.T, env *cogtest.Env, username string) int64 {
	t.Helper()
	store := feedStore(t, env, username)
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	space, err := feedPlugin(t, env).Space(username)
	require.NoError(t, err)
	n, err := space.Own.Put("media/1.mp3", strings.NewReader("unclean bytes"))
	require.NoError(t, err)
	require.NoError(t, store.EnqueueDownload(id))
	require.NoError(t, store.MarkDownloadStart(id))
	require.NoError(t, store.MarkDownloadReady(id, "media/1.mp3", n))
	muteAll(t, store, id)
	return id
}

// uncleanVideo is a packaged video a mute filter matched, not cleaned yet.
func uncleanVideo(t *testing.T, env *cogtest.Env, username string) int64 {
	t.Helper()
	id := packagedItem(t, env, username, true)
	muteAll(t, feedStore(t, env, username), id)
	return id
}

func resolveErr(t *testing.T, env *cogtest.Env, u *cogtest.User, id int64, v feedpb.Variant) error {
	t.Helper()
	res, err := feedClient(t, env, u.AppPassword(t, "phone")).ResolveMedia(context.Background(),
		connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id, Variant: v}))
	if err == nil {
		t.Fatalf("item %d (%v) plays %q before it is clean", id, v, res.Msg.Url)
	}
	require.Equal(t, connect.CodeUnavailable, connect.CodeOf(err))
	return err
}

// A downloaded item to be muted plays nothing until it is clean: not its
// download, not its packaged presentation, whichever variant is asked for.
func TestResolveMediaUncleanedWaits(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	noStream(t)
	ep := uncleanEpisode(t, env, "alice")
	video := uncleanVideo(t, env, "alice")

	for _, c := range []struct {
		id int64
		v  feedpb.Variant
	}{{ep, feedpb.Variant_VARIANT_UNSPECIFIED}, {video, feedpb.Variant_VARIANT_VIDEO}, {video, feedpb.Variant_VARIANT_AUDIO}} {
		err := resolveErr(t, env, u, c.id, c.v)
		assert.True(t, strings.Contains(err.Error(), "Cleaning the audio"), err.Error())
	}
}

// Why an item waits says when cleaning failed, or cannot happen.
func TestResolveMediaUncleanedSaysWhy(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	id := uncleanEpisode(t, env, "alice")

	require.NoError(t, store.MarkTranscribeError(id, "whisper-cli: exit status 1\nlots of output"))
	err := resolveErr(t, env, u, id, feedpb.Variant_VARIANT_UNSPECIFIED)
	assert.True(t, strings.Contains(err.Error(), "Cleaning failed: whisper-cli: exit status 1"), err.Error())
	assert.False(t, strings.Contains(err.Error(), "lots of output"), err.Error())

	require.NoError(t, store.MarkProfanityError(id, "tts: not configured"))
	err = resolveErr(t, env, u, id, feedpb.Variant_VARIANT_UNSPECIFIED)
	assert.True(t, strings.Contains(err.Error(), "Cleaning failed: tts: not configured"), err.Error())
}

// Transcription that ends without captions, as when whisper is off, leaves
// nothing to clean by; the reason says so rather than "Cleaning".
func TestResolveMediaUncleanedNoCaptions(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	id := uncleanEpisode(t, env, "alice")
	require.NoError(t, feedStore(t, env, "alice").ClearNeedsTranscribe(id))

	err := resolveErr(t, env, u, id, feedpb.Variant_VARIANT_UNSPECIFIED)
	assert.True(t, strings.Contains(err.Error(), "Cleaning failed: no captions"), err.Error())
}

// Without the profanity plugin nothing cleans; the reason says so.
func TestResolveMediaUncleanedNoProfanityPlugin(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	id := uncleanEpisode(t, env, "alice")

	err := resolveErr(t, env, u, id, feedpb.Variant_VARIANT_UNSPECIFIED)
	assert.True(t, strings.Contains(err.Error(), "Cleaning failed: the profanity plugin is not installed"), err.Error())
}

func TestStreamURLUncleanedWaits(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	noStream(t)
	ep := uncleanEpisode(t, env, "alice")
	video := uncleanVideo(t, env, "alice")

	for _, path := range []string{
		"/apps/feed/api/stream-url/" + strconv.FormatInt(ep, 10),
		"/apps/feed/api/stream-url/" + strconv.FormatInt(video, 10),
		"/apps/feed/api/stream-url/" + strconv.FormatInt(video, 10) + "?variant=audio",
	} {
		var got map[string]any
		env.Get(t, path, u.Auth).Status(http.StatusOK).JSON(&got)
		assert.Equal[any](t, true, got["downloading"], path)
		assert.Equal[any](t, "Cleaning the audio", got["message"], path)
		assert.Equal[any](t, nil, got["url"], path)
		assert.Equal[any](t, nil, got["hls"], path)
		assert.Equal[any](t, nil, got["failed"], path)
	}
}

func TestItemMediaUncleanedRefuses(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	id := uncleanEpisode(t, env, "alice")

	body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", u.Auth).Status(http.StatusServiceUnavailable).ReadBody()
	assert.True(t, strings.Contains(body, "Cleaning the audio"), body)
	assert.False(t, strings.Contains(body, "unclean bytes"), body)
}

// The media route of a muted episode Cog has not downloaded queues it,
// retrying one that failed, and says why.
func TestItemMediaMutedQueues(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	muteAll(t, store, id)
	require.NoError(t, store.MarkDownloadStart(id))
	require.NoError(t, store.MarkDownloadError(id, "HTTP 404"))

	body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", u.Auth).Status(http.StatusServiceUnavailable).ReadBody()
	assert.True(t, strings.Contains(body, "Downloading the audio again; the last try failed: HTTP 404"), body)
	requireQueued(t, store, id)
}

// A packaged presentation carries the original audio. Links to it do not
// expire, so every file of it is refused while the item has no clean track.
func TestHLSUncleanedRefused(t *testing.T) {
	env := cleaningEnv(t)
	env.CreateUser(t, "alice", "pw")
	id := packagedItem(t, env, "alice", true)
	master := masterURL(t, env, "alice", id)
	files := []string{master, sibling(master, "audio.m3u8"), sibling(master, "audio.m4a"), sibling(master, "video.m3u8"), sibling(master, "video.mp4")}
	for _, f := range files {
		env.Get(t, f).Status(http.StatusOK)
	}

	store := feedStore(t, env, "alice")
	muteAll(t, store, id)
	for _, f := range files {
		env.Get(t, f).Status(http.StatusServiceUnavailable)
	}
	// Clean audio not yet packaged as a track: the presentation still has
	// only the original.
	require.NoError(t, store.SetCleanAudioPath(id, "media/clean.m4a"))
	for _, f := range files {
		env.Get(t, f).Status(http.StatusServiceUnavailable)
	}
}

func TestM3UUncleanedLeftOut(t *testing.T) {
	env := cleaningEnv(t)
	env.CreateUser(t, "alice", "pw")
	store := feedStore(t, env, "alice")
	unclean := uncleanVideo(t, env, "alice")
	pending := uncleanVideo(t, env, "alice")
	require.NoError(t, store.SetCleanAudioPath(pending, "media/clean.m4a"))
	podcastEpisode(t, store, "Plain")

	body := env.Get(t, m3uURL(t, env, "alice", "unread/all.m3u", false)).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, "Plain"), body)
	for _, id := range []int64{unclean, pending} {
		assert.False(t, strings.Contains(body, "/apps/feed/hls/alice/"+strconv.FormatInt(id, 10)+"/"), body)
	}
}

// The web player of an item that is waiting says why rather than load
// media it cannot play.
func TestPlayerUncleanedWaits(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	ep := uncleanEpisode(t, env, "alice")
	video := uncleanVideo(t, env, "alice")

	for _, id := range []int64{ep, video} {
		body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(id, 10), u.Auth).Status(http.StatusOK).ReadBody()
		assert.False(t, strings.Contains(body, `src="/apps/feed/items/`+strconv.FormatInt(id, 10)+`/media"`), body)
		assert.False(t, strings.Contains(body, "data-hls="), body)
		assert.True(t, strings.Contains(body, `data-waiting="Cleaning the audio"`), body)
	}
}

// cleaner is a Processor that finds nothing to mute (none) or writes clean
// audio.
type cleaner struct{ none bool }

func (c cleaner) Process(_ context.Context, _, out string, _ []cogplugin.CaptionSegment) error {
	if c.none {
		return profanity.ErrNoRanges
	}
	return os.WriteFile(out, []byte("clean bytes"), 0o644)
}

// cleanOnce gives an unclean episode captions and runs the profanity worker
// over it with c.
func cleanOnce(t *testing.T, env *cogtest.Env, username string, id int64, c cleaner) {
	t.Helper()
	store := feedStore(t, env, username)
	space, err := feedPlugin(t, env).Space(username)
	require.NoError(t, err)
	_, err = space.Own.Put("captions/1.vtt", strings.NewReader("WEBVTT\n\n00:00:00.000 --> 00:00:02.000\nhello world\n"))
	require.NoError(t, err)
	require.NoError(t, store.SetCaptionsPath(id, "captions/1.vtt"))
	require.NoError(t, pipeline.NewProfanity(store, space, c).RunOnce(context.Background()))
}

// Every way cleaning succeeds ends with the item playable: nothing to mute
// plays the download, which is clean as it is.
func TestUncleanedPlaysWhenNothingToMute(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	id := uncleanEpisode(t, env, "alice")
	cleanOnce(t, env, "alice", id, cleaner{none: true})

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	require.NoError(t, err)
	assert.Equal(t, "/apps/feed/items/"+strconv.FormatInt(id, 10)+"/media", res.Msg.Url)
	assert.False(t, res.Msg.Cleaned)
	body := env.Get(t, res.Msg.Url, u.Auth).Status(http.StatusOK).ReadBody()
	assert.Equal(t, "unclean bytes", body)
}

// ... and cleaned audio plays the clean file.
func TestUncleanedPlaysWhenCleaned(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	id := uncleanEpisode(t, env, "alice")
	cleanOnce(t, env, "alice", id, cleaner{})

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ResolveMedia(context.Background(), connect.NewRequest(&feedpb.ResolveMediaRequest{ItemId: id}))
	require.NoError(t, err)
	assert.True(t, res.Msg.Cleaned)
	body := env.Get(t, res.Msg.Url, u.Auth).Status(http.StatusOK).ReadBody()
	assert.Equal(t, "clean bytes", body)
}

// A cleaning failure reaches the web player and the media route as
// "Cleaning failed", which the player takes as final: it stops asking.
func TestStreamURLCleaningFailed(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	id := uncleanEpisode(t, env, "alice")
	media := "/apps/feed/items/" + strconv.FormatInt(id, 10) + "/media"

	for _, c := range []struct {
		mark func() error
		want string
	}{
		{func() error { return store.MarkTranscribeError(id, "whisper-cli: exit status 1\nusage: …") }, "Cleaning failed: whisper-cli: exit status 1"},
		{func() error { return store.MarkProfanityError(id, "tts: not configured") }, "Cleaning failed: tts: not configured"},
	} {
		require.NoError(t, c.mark())
		got := streamURL(t, env, u, id)
		assert.Equal[any](t, true, got["downloading"])
		assert.Equal[any](t, c.want, got["message"])
		assert.Equal[any](t, true, got["failed"])
		body := env.Get(t, media, u.Auth).Status(http.StatusServiceUnavailable).ReadBody()
		assert.Equal(t, c.want+"\n", body)
	}
}

// A playlist, which players queue from, lists an item only once it can
// play; the lists a screen browses still show it.
func TestPlaylistLeavesOutUncleaned(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	waiting := uncleanEpisode(t, env, "alice")
	plain := podcastEpisode(t, store, "Plain")
	plID, err := store.CreatePlaylist(feed.Playlist{Name: "Pod", Query: "type:audio", Order: feed.OrderOldest})
	require.NoError(t, err)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	pl := &feedpb.View{Kind: &feedpb.View_PlaylistId{PlaylistId: plID}}

	got, total := listAll(t, client, &feedpb.ListItemsRequest{View: pl, UnfinishedOnly: true})
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []int64{plain}, got)
	got, _ = listAll(t, client, &feedpb.ListItemsRequest{})
	assert.Equal(t, 2, len(got))

	require.NoError(t, store.SetCleanAudioPath(waiting, "media/1.clean.mp3"))
	require.NoError(t, store.ClearNeedsProfanity(waiting))
	got, total = listAll(t, client, &feedpb.ListItemsRequest{View: pl, UnfinishedOnly: true})
	assert.Equal(t, int64(2), total)
	assert.Equal(t, 2, len(got))
}

// The web player's next and previous step over an item that cannot play
// yet, in any list.
func TestAdjacentPlayerSkipsUncleaned(t *testing.T) {
	env := cleaningEnv(t)
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, "alice")
	sub, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/adj", Title: "Pod"})
	require.NoError(t, err)
	var ids []int64
	for i, g := range []string{"a", "w", "c"} {
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: sub, GUID: g, Type: "audio", Title: strings.ToUpper(g),
			MediaURL: "https://pod.example.com/" + g + ".mp3", PublishedAt: int64(100 * (i + 1))})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.NoError(t, store.EnqueueProfanity(ids[1]))

	// Newest first: C, W, A.
	body := env.Get(t, "/apps/feed/items/"+strconv.FormatInt(ids[2], 10)+"/adjacent?dir=next&ctx=all", u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-item-id="`+strconv.FormatInt(ids[0], 10)+`"`), body)
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(ids[0], 10)+"/adjacent?dir=prev&ctx=all", u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-item-id="`+strconv.FormatInt(ids[2], 10)+`"`), body)
}
