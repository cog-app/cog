package feed_test

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb/feedpbconnect"
)

// feedClient is a FeedService client signed in with authorization, over
// gRPC (HTTP/2) unless opts say otherwise.
func feedClient(t *testing.T, env *cogtest.Env, authorization string, opts ...connect.ClientOption) feedpbconnect.FeedServiceClient {
	t.Helper()
	c, base := env.RPCClient(t, authorization)
	return feedpbconnect.NewFeedServiceClient(c, base, append([]connect.ClientOption{connect.WithGRPC()}, opts...)...)
}

func TestGetLibraryOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)

	folderID, err := store.CreateFolder("News")
	require.NoError(t, err)
	subID, err := store.CreateSubscription(feed.Subscription{
		Kind: "rss", URL: "https://news.example.com/feed", Title: "Daily",
		FolderID: sql.NullInt64{Int64: folderID, Valid: true},
	})
	require.NoError(t, err)
	var ids []int64
	for _, guid := range []string{"a", "b", "c"} {
		id, _, err := store.UpsertItem(feed.Item{SubscriptionID: subID, GUID: guid, Type: "audio", Title: guid})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	require.NoError(t, store.MarkRead(ids[0], true))
	require.NoError(t, store.MarkStarred(ids[1], true))
	require.NoError(t, store.AddTag(ids[1], "later"))
	require.NoError(t, store.AddTag(ids[2], "later"))
	require.NoError(t, store.EnqueueDownload(ids[2]))
	require.NoError(t, store.MarkDownloadReady(ids[2], "/x/c.mp3", 10))
	_, err = store.CreatePlaylist(feed.Playlist{Name: "Commute", Query: "type:audio"})
	require.NoError(t, err)

	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
	require.NoError(t, err)
	lib := res.Msg

	assert.Equal(t, &feedpb.Counts{All: 3, Unread: 2, Starred: 1, Downloaded: 1}, lib.Counts)
	require.Equal(t, 1, len(lib.Folders))
	assert.Equal(t, "News", lib.Folders[0].Name)
	assert.Equal(t, int64(2), lib.Folders[0].Unread)
	require.Equal(t, 1, len(lib.Subscriptions))
	sub := lib.Subscriptions[0]
	assert.Equal(t, feedpb.SubscriptionKind_SUBSCRIPTION_KIND_RSS, sub.Kind)
	assert.Equal(t, folderID, sub.GetFolderId())
	assert.Equal(t, int64(2), sub.Unread)
	require.Equal(t, 1, len(lib.Playlists))
	assert.Equal(t, "type:audio", lib.Playlists[0].Query)
	require.Equal(t, 1, len(lib.Tags))
	assert.Equal(t, "later", lib.Tags[0].Name)
	assert.Equal(t, int64(2), lib.Tags[0].Count)
}

func TestFeedServiceSpeaksConnectOverHTTP1(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	// Plain HTTP/1.1, which is what a proxy that does not pass HTTP/2
	// through leaves a client with.
	srv := httptest.NewServer(env.Handler)
	t.Cleanup(srv.Close)
	client := feedpbconnect.NewFeedServiceClient(srv.Client(), srv.URL,
		connect.WithProtoJSON(), withHeader("Authorization", u.AppPassword(t, "tui")))
	res, err := client.GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Msg.Counts.All)
}

// withHeader sets a header on every request a client sends.
func withHeader(key, value string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set(key, value)
				return next(ctx, req)
			}
		}))
}

func TestFeedServiceRefusesTheSessionCookie(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	// A browser sends the cookie with a cross-site request; only a credential
	// a page cannot attach may call the service.
	client := feedClient(t, env, "", withHeader("Cookie", auth.SessionCookieName+"="+sess.ID))
	_, err = client.GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))

	_, err = feedClient(t, env, "Bearer wrong").
		GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

func TestFeedServiceUnimplementedRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	_, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ExportOPML(context.Background(), connect.NewRequest(&feedpb.ExportOPMLRequest{}))
	assert.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestItemStateOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	got, err := client.GetItem(ctx, connect.NewRequest(&feedpb.GetItemRequest{Id: id}))
	require.NoError(t, err)
	assert.Equal(t, "Episode 1", got.Msg.Item.Title)
	assert.Equal(t, "Test Podcast", got.Msg.Item.SubscriptionTitle)
	assert.Equal(t, feedpb.ItemType_ITEM_TYPE_AUDIO, got.Msg.Item.Type)

	item, err := client.SetRead(ctx, connect.NewRequest(&feedpb.SetReadRequest{Id: id, Read: true}))
	require.NoError(t, err)
	assert.True(t, item.Msg.Read)
	item, err = client.SetStarred(ctx, connect.NewRequest(&feedpb.SetStarredRequest{Id: id, Starred: true}))
	require.NoError(t, err)
	assert.True(t, item.Msg.Starred)

	item, err = client.SetTags(ctx, connect.NewRequest(&feedpb.SetTagsRequest{Id: id, Add: []string{" later ", "car"}}))
	require.NoError(t, err)
	assert.Equal(t, []string{"car", "later"}, item.Msg.Tags)
	item, err = client.SetTags(ctx, connect.NewRequest(&feedpb.SetTagsRequest{Id: id, Remove: []string{"car"}}))
	require.NoError(t, err)
	assert.Equal(t, []string{"later"}, item.Msg.Tags)
	_, err = client.SetTags(ctx, connect.NewRequest(&feedpb.SetTagsRequest{Id: id, Add: []string{"  "}}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	stored, err := store.GetItem(id)
	require.NoError(t, err)
	assert.True(t, stored.IsRead && stored.IsStarred)

	for _, call := range []func() error{
		func() error {
			_, err := client.GetItem(ctx, connect.NewRequest(&feedpb.GetItemRequest{Id: id + 100}))
			return err
		},
		func() error {
			_, err := client.SetRead(ctx, connect.NewRequest(&feedpb.SetReadRequest{Id: id + 100, Read: true}))
			return err
		},
	} {
		assert.Equal(t, connect.CodeNotFound, connect.CodeOf(call()))
	}
}

func TestReportProgressOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	id := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	now := time.Now()
	pb, err := client.ReportProgress(ctx, connect.NewRequest(&feedpb.ReportProgressRequest{
		ItemId: id, ProgressSec: 300, At: timestamppb.New(now.Add(-time.Minute)),
	}))
	require.NoError(t, err)
	assert.Equal(t, 300.0, pb.Msg.ProgressSec)

	// Offline progress from an hour ago arrives late and loses; the reply
	// says what is stored.
	pb, err = client.ReportProgress(ctx, connect.NewRequest(&feedpb.ReportProgressRequest{
		ItemId: id, ProgressSec: 120, At: timestamppb.New(now.Add(-time.Hour)),
	}))
	require.NoError(t, err)
	assert.Equal(t, 300.0, pb.Msg.ProgressSec)

	pb, err = client.ReportProgress(ctx, connect.NewRequest(&feedpb.ReportProgressRequest{ItemId: id, Completed: true}))
	require.NoError(t, err)
	assert.True(t, pb.Msg.Completed)
	item, err := client.GetItem(ctx, connect.NewRequest(&feedpb.GetItemRequest{Id: id}))
	require.NoError(t, err)
	assert.True(t, item.Msg.Item.Read)
	assert.True(t, item.Msg.Item.Playback.Completed)
}
