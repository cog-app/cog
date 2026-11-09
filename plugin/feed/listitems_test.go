package feed_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb/feedpbconnect"
)

// listAll pages through ListItems and returns every item's id, and the
// total the first page gave.
func listAll(t *testing.T, client feedpbconnect.FeedServiceClient, req *feedpb.ListItemsRequest) ([]int64, int64) {
	t.Helper()
	var ids []int64
	var total int64
	for page := 0; ; page++ {
		res, err := client.ListItems(context.Background(), connect.NewRequest(req))
		require.NoError(t, err)
		if page == 0 {
			total = res.Msg.Total
		} else {
			assert.Equal(t, int64(0), res.Msg.Total, "total on a later page")
		}
		for _, it := range res.Msg.Items {
			ids = append(ids, it.Id)
		}
		if res.Msg.NextPageToken == "" {
			return ids, total
		}
		require.True(t, page < 1000, "pages never end")
		req.PageToken = res.Msg.NextPageToken
	}
}

func TestListItemsOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	ids, video, text := playlistItems(t, store)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	got, total := listAll(t, client, &feedpb.ListItemsRequest{PageSize: 2})
	assert.Equal(t, int64(6), total)
	assert.Equal(t, []int64{ids[4], ids[3], ids[2], ids[1], text, ids[0]}, got) // newest first; the text item ties with ep0 and has the larger id

	// A playlist lists in its order; unfinished_only is its play queue.
	require.NoError(t, store.SetPlayback(ids[1], 40*60, true, time.Now()))
	plID, err := store.CreatePlaylist(feed.Playlist{Name: "Pod", Query: "type:audio OR type:video", Order: feed.OrderOldest})
	require.NoError(t, err)
	pl := &feedpb.View{Kind: &feedpb.View_PlaylistId{PlaylistId: plID}}
	got, total = listAll(t, client, &feedpb.ListItemsRequest{View: pl, PageSize: 2, UnfinishedOnly: true})
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []int64{ids[0], ids[2], ids[3], video}, got)
	// The query narrows the view.
	got, _ = listAll(t, client, &feedpb.ListItemsRequest{View: pl, Query: "type:video"})
	assert.Equal(t, []int64{video}, got)

	// History is by when it was played.
	require.NoError(t, store.SetPlayback(ids[3], 60, false, time.Now().Add(time.Minute)))
	got, _ = listAll(t, client, &feedpb.ListItemsRequest{View: &feedpb.View{Kind: &feedpb.View_History{History: true}}, PageSize: 1})
	assert.Equal(t, []int64{ids[3], ids[1]}, got)

	// A token is for the order it came from.
	res, err := client.ListItems(ctx, connect.NewRequest(&feedpb.ListItemsRequest{View: pl, PageSize: 1}))
	require.NoError(t, err)
	p, err := store.GetPlaylist(plID)
	require.NoError(t, err)
	p.Order = feed.OrderNewest
	require.NoError(t, store.UpdatePlaylist(p))
	_, err = client.ListItems(ctx, connect.NewRequest(&feedpb.ListItemsRequest{View: pl, PageSize: 1, PageToken: res.Msg.NextPageToken}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	for _, bad := range []*feedpb.ListItemsRequest{
		{Query: "(type:audio"},
		{Query: "type>3"},
		{PageSize: -1},
		{PageToken: "not a token"},
	} {
		_, err := client.ListItems(ctx, connect.NewRequest(bad))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%+v", bad)
	}
	_, err = client.ListItems(ctx, connect.NewRequest(&feedpb.ListItemsRequest{View: &feedpb.View{Kind: &feedpb.View_PlaylistId{PlaylistId: plID + 100}}}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

// A search the SQL can only narrow is refined a batch at a time: a sparse
// one still fills its pages and finds every match.
func TestListItemsPagesASparseSearch(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://blog.example.com/feed", Title: "Blog"})
	require.NoError(t, err)
	var want []int64
	for i := range 450 {
		title := "post " + strconv.Itoa(i)
		if i%100 == 7 {
			title = "needle " + strconv.Itoa(i)
		}
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: strconv.Itoa(i), Type: "text", Title: title, PublishedAt: int64(1000 - i)})
		require.NoError(t, err)
		if i%100 == 7 {
			want = append(want, id)
		}
	}
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	got, total := listAll(t, client, &feedpb.ListItemsRequest{Query: "needle", PageSize: 2})
	assert.Equal(t, int64(5), total)
	assert.Equal(t, want, got)

	// Marking a search's items read reaches every match and nothing else.
	n, err := store.MarkViewRead(feed.ViewSpec{All: true}, mustParse(t, "needle"), time.Now())
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
	res, err := client.GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int64(445), res.Msg.Counts.Unread)
}

func TestMarkAllReadOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	ids, video, text := playlistItems(t, store)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()
	require.NoError(t, store.MarkRead(ids[0], true))

	plID, err := store.CreatePlaylist(feed.Playlist{Name: "Audio", Query: "type:audio"})
	require.NoError(t, err)
	res, err := client.MarkAllRead(ctx, connect.NewRequest(&feedpb.MarkAllReadRequest{View: &feedpb.View{Kind: &feedpb.View_PlaylistId{PlaylistId: plID}}}))
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Msg.Changed) // ep0 was read already
	for _, id := range []int64{video, text} {
		it, err := store.GetItem(id)
		require.NoError(t, err)
		assert.False(t, it.IsRead, "item %d outside the playlist was marked", id)
	}

	res, err = client.MarkAllRead(ctx, connect.NewRequest(&feedpb.MarkAllReadRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Msg.Changed)

	_, err = client.MarkAllRead(ctx, connect.NewRequest(&feedpb.MarkAllReadRequest{View: &feedpb.View{Kind: &feedpb.View_PlaylistId{PlaylistId: plID + 100}}}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func mustParse(t *testing.T, q string) query.Expr {
	t.Helper()
	e, err := query.Parse(q)
	require.NoError(t, err)
	return e
}
