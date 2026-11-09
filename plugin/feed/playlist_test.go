package feed_test

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

// playlistItems seeds a subscription with five items and returns their IDs:
// published a day apart, oldest first, with durations 50, 40, 30, 20, 10
// minutes, the last one a video and one more a text item.
func playlistItems(t *testing.T, store *feed.Store) (ids []int64, video, text int64) {
	t.Helper()
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/feed", Title: "Pod"})
	require.NoError(t, err)
	day := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	for i := range 5 {
		typ := "audio"
		if i == 4 {
			typ = "video"
		}
		id, _, err := store.UpsertItem(feed.Item{
			SubscriptionID: subID, GUID: strconv.Itoa(i), Type: typ, Title: "ep" + strconv.Itoa(i),
			PublishedAt: day + int64(i)*86400, DurationSec: int64(50-10*i) * 60,
			MediaURL: "https://pod.example.com/" + strconv.Itoa(i) + ".mp3",
		})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	text, _, err = store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: "post", Type: "text", Title: "post", PublishedAt: day})
	require.NoError(t, err)
	return ids, ids[4], text
}

func itemIDs(items []feed.ItemWithSubscription) []int64 {
	out := make([]int64, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestPlaylistPlaysInItsOrder(t *testing.T) {
	s := newStore(t)
	ids, _, _ := playlistItems(t, s)
	now := time.Now()
	list := func(pl feed.Playlist) []int64 {
		t.Helper()
		items, err := s.PlaylistItems(pl, feed.ViewSpec{}, now, 100)
		require.NoError(t, err)
		return itemIDs(items)
	}
	pl := feed.Playlist{ID: 1, Query: "type:audio"}

	pl.Order = feed.OrderNewest
	assert.Equal(t, []int64{ids[3], ids[2], ids[1], ids[0]}, list(pl))
	pl.Order = feed.OrderOldest
	assert.Equal(t, []int64{ids[0], ids[1], ids[2], ids[3]}, list(pl))

	// Shortest is the least time left: ep0 is 50 minutes long but 45 of
	// them are played.
	require.NoError(t, s.SetPlayback(ids[0], 45*60, false, now))
	pl.Order = feed.OrderShortest
	assert.Equal(t, []int64{ids[0], ids[3], ids[2], ids[1]}, list(pl))

	// A shuffle holds still for its seed, and another seed reorders it.
	pl.Order, pl.ShuffleSeed = feed.OrderShuffled, 7
	first := list(pl)
	assert.Equal(t, first, list(pl))
	sorted := slices.Clone(first)
	slices.Sort(sorted)
	assert.Equal(t, []int64{ids[0], ids[1], ids[2], ids[3]}, sorted)
	moved := false
	for seed := int64(8); seed < 20 && !moved; seed++ {
		pl.ShuffleSeed = seed
		moved = !slices.Equal(first, list(pl))
	}
	assert.True(t, moved, "no other seed changed the order")
}

func TestUnfinishedKeepsMediaNotPlayedToTheEnd(t *testing.T) {
	s := newStore(t)
	ids, _, text := playlistItems(t, s)
	require.NoError(t, s.SetPlayback(ids[1], 40*60, true, time.Now()))
	require.NoError(t, s.SetPlayback(ids[2], 60, false, time.Now()))
	items, err := s.ListItemsForView(feed.ViewSpec{All: true, Unfinished: true, Order: feed.OrderOldest}, 100)
	require.NoError(t, err)
	got := itemIDs(items)
	assert.Equal(t, []int64{ids[0], ids[2], ids[3], ids[4]}, got)
	assert.False(t, slices.Contains(got, text))
}

func TestPlaylistCounts(t *testing.T) {
	s := newStore(t)
	ids, _, _ := playlistItems(t, s)
	require.NoError(t, s.SetPlayback(ids[0], 50*60, true, time.Now()))
	require.NoError(t, s.SetPlayback(ids[1], 10*60, false, time.Now()))
	exact := feed.Playlist{ID: 1, Query: "type:audio"}
	// A text search needs the refine step, so it is counted from its items.
	refined := feed.Playlist{ID: 2, Query: "ep1 OR ep2"}
	empty := feed.Playlist{ID: 3, Query: "type:none"}
	counts, err := s.PlaylistCounts([]feed.Playlist{exact, refined, empty}, time.Now(), 100)
	require.NoError(t, err)
	// ep0 is finished; ep1 has 30 of 40 minutes left, ep2 30, ep3 20.
	assert.Equal(t, feed.PlaylistCounts{Items: 4, Unfinished: 3, UnfinishedSec: 80 * 60}, counts[1])
	assert.Equal(t, feed.PlaylistCounts{Items: 2, Unfinished: 2, UnfinishedSec: 60 * 60}, counts[2])
	assert.Equal(t, feed.PlaylistCounts{}, counts[3])
}

func TestSavePlaylistOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	ids, _, _ := playlistItems(t, store)
	require.NoError(t, store.SetPlayback(ids[0], 50*60, true, time.Now()))
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()
	save := func(pl *feedpb.Playlist) (*feedpb.Playlist, error) {
		res, err := client.SavePlaylist(ctx, connect.NewRequest(pl))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}

	pl, err := save(&feedpb.Playlist{Name: " Commute ", Query: "type:audio", ListenOnly: true})
	require.NoError(t, err)
	assert.Equal(t, "Commute", pl.Name)
	assert.Equal(t, feedpb.PlayOrder_PLAY_ORDER_NEWEST, pl.Order)
	assert.True(t, pl.ListenOnly)
	assert.Equal(t, int64(4), pl.Count)
	assert.Equal(t, int64(3), pl.UnfinishedCount)
	assert.Equal(t, int64(90*60), pl.UnfinishedDurationSec)

	// Shuffled with no seed is a reshuffle: the server picks one.
	pl.Order, pl.ShuffleSeed, pl.ListenOnly = feedpb.PlayOrder_PLAY_ORDER_SHUFFLED, 0, false
	pl, err = save(pl)
	require.NoError(t, err)
	assert.True(t, pl.ShuffleSeed != 0, "no seed picked")
	stored, err := store.GetPlaylist(pl.Id)
	require.NoError(t, err)
	assert.Equal(t, feed.OrderShuffled, stored.Order)
	assert.Equal(t, pl.ShuffleSeed, stored.ShuffleSeed)
	assert.False(t, stored.ListenOnly)

	res, err := client.GetLibrary(ctx, connect.NewRequest(&feedpb.GetLibraryRequest{}))
	require.NoError(t, err)
	require.Equal(t, 1, len(res.Msg.Playlists))
	assert.Equal(t, int64(3), res.Msg.Playlists[0].UnfinishedCount)
	assert.Equal(t, pl.ShuffleSeed, res.Msg.Playlists[0].ShuffleSeed)

	for _, bad := range []struct {
		pl   *feedpb.Playlist
		code connect.Code
	}{
		{&feedpb.Playlist{Name: " ", Query: "type:audio"}, connect.CodeInvalidArgument},
		{&feedpb.Playlist{Name: "x", Query: ""}, connect.CodeInvalidArgument},
		{&feedpb.Playlist{Name: "x", Query: "(type:audio"}, connect.CodeInvalidArgument},
		{&feedpb.Playlist{Name: "x", Query: "type:audio", Order: 99}, connect.CodeInvalidArgument},
		{&feedpb.Playlist{Name: "x", Query: "type:audio", ShuffleSeed: -1}, connect.CodeInvalidArgument},
		{&feedpb.Playlist{Name: "Commute", Query: "type:video"}, connect.CodeAlreadyExists},
		{&feedpb.Playlist{Id: pl.Id + 100, Name: "x", Query: "type:audio"}, connect.CodeNotFound},
	} {
		_, err := save(bad.pl)
		assert.Equal(t, bad.code, connect.CodeOf(err), "%+v", bad.pl)
	}

	_, err = client.DeletePlaylist(ctx, connect.NewRequest(&feedpb.DeletePlaylistRequest{Id: pl.Id}))
	require.NoError(t, err)
	_, err = client.DeletePlaylist(ctx, connect.NewRequest(&feedpb.DeletePlaylistRequest{Id: pl.Id}))
	require.NoError(t, err)
	all, err := store.ListPlaylists()
	require.NoError(t, err)
	assert.Equal(t, 0, len(all))
}

// postPlaylistPlay posts the playlists page's order form.
func postPlaylistPlay(t *testing.T, env *cogtest.Env, u *cogtest.User, id int64, form url.Values) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	form.Set("csrf_token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))
	env.PostForm(t, "/apps/feed/playlists/"+strconv.FormatInt(id, 10)+"/play", form,
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID), cogtest.StopRedirect,
	).Status(http.StatusSeeOther)
}

func TestWebPlaylistOrderAndListenOnly(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	ids, video, _ := playlistItems(t, store)
	id, err := store.CreatePlaylist(feed.Playlist{Name: "All media", Query: "type:audio OR type:video"})
	require.NoError(t, err)

	postPlaylistPlay(t, env, u, id, url.Values{"order": {"oldest"}, "listen_only": {"on"}})
	pl, err := store.GetPlaylist(id)
	require.NoError(t, err)
	assert.Equal(t, feed.OrderOldest, pl.Order)
	assert.True(t, pl.ListenOnly)

	// The playlist page lists oldest first, and the player's next follows.
	body := env.Get(t, "/apps/feed/playlists/"+strconv.FormatInt(id, 10), u.Auth).Status(http.StatusOK).ReadBody()
	first := strings.Index(body, ">ep0<")
	last := strings.Index(body, ">ep4<")
	assert.True(t, first >= 0 && last > first, "ep0 not listed before ep4")

	ctx := "?ctx=playlist&cid=" + strconv.FormatInt(id, 10)
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(ids[0], 10)+ctx, u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-bar-next-id="`+strconv.FormatInt(ids[1], 10)+`"`), "next is not ep1")

	// A video in a listen-only playlist plays in an audio element.
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(video, 10)+ctx, u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, `data-variant="audio"`), "no audio variant")
	assert.True(t, strings.Contains(body, "<audio"), "no audio element")
	assert.False(t, strings.Contains(body, "<video"), "video element in a listen-only playlist")
	// Outside the playlist it is a video again.
	body = env.Get(t, "/apps/feed/items/"+strconv.FormatInt(video, 10), u.Auth).Status(http.StatusOK).ReadBody()
	assert.True(t, strings.Contains(body, "<video"), "no video element")

	postPlaylistPlay(t, env, u, id, url.Values{"reshuffle": {"1"}})
	pl, err = store.GetPlaylist(id)
	require.NoError(t, err)
	assert.Equal(t, feed.OrderShuffled, pl.Order)
	assert.True(t, pl.ListenOnly, "a reshuffle changed listen only")
}
