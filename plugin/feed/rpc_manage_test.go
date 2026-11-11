package feed_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

func rssServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, err := w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Feed Title</title></channel></rss>`))
		if err != nil {
			t.Errorf("write feed: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSubscriptionsOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()
	srv := rssServer(t)
	missing := int64(999)

	_, err := client.AddSubscription(ctx, connect.NewRequest(&feedpb.AddSubscriptionRequest{Url: "ftp://example.com/feed"}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = client.AddSubscription(ctx, connect.NewRequest(&feedpb.AddSubscriptionRequest{Url: srv.URL, FolderId: &missing}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	folder, err := client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Name: " News "}))
	require.NoError(t, err)
	assert.Equal(t, "News", folder.Msg.Name)

	sub, err := client.AddSubscription(ctx, connect.NewRequest(&feedpb.AddSubscriptionRequest{Url: srv.URL, FolderId: &folder.Msg.Id}))
	require.NoError(t, err)
	assert.Equal(t, feedpb.SubscriptionKind_SUBSCRIPTION_KIND_RSS, sub.Msg.Kind)
	assert.Equal(t, folder.Msg.Id, sub.Msg.GetFolderId())
	id := sub.Msg.Id
	_, err = client.AddSubscription(ctx, connect.NewRequest(&feedpb.AddSubscriptionRequest{Url: srv.URL}))
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))

	// A title the user gives stays when the feed is fetched again.
	title, interval, paused, none := "Mine", int64(3600), true, int64(0)
	got, err := client.UpdateSubscription(ctx, connect.NewRequest(&feedpb.UpdateSubscriptionRequest{
		Id: id, Title: &title, PollIntervalSec: &interval, Paused: &paused, FolderId: &none,
	}))
	require.NoError(t, err)
	assert.Equal(t, "Mine", got.Msg.Title)
	assert.Equal(t, int64(3600), got.Msg.PollIntervalSec)
	assert.True(t, got.Msg.Paused)
	assert.Nil(t, got.Msg.FolderId)
	require.NoError(t, store.UpdateSubscriptionMetadata(id, map[string]string{"title": "Feed Title", "description": "about"}))
	stored, err := store.GetSubscription(id)
	require.NoError(t, err)
	assert.Equal(t, "Mine", stored.Title)
	assert.Equal(t, "about", stored.Description)

	// Clearing it hands the title back to the feed, from a full fetch.
	cleared := ""
	_, err = client.UpdateSubscription(ctx, connect.NewRequest(&feedpb.UpdateSubscriptionRequest{Id: id, Title: &cleared}))
	require.NoError(t, err)
	require.NoError(t, store.UpdateSubscriptionMetadata(id, map[string]string{"title": "Feed Title"}))
	stored, err = store.GetSubscription(id)
	require.NoError(t, err)
	assert.Equal(t, "Feed Title", stored.Title)
	assert.Equal(t, "", stored.ETag)

	short := int64(10)
	_, err = client.UpdateSubscription(ctx, connect.NewRequest(&feedpb.UpdateSubscriptionRequest{Id: id, PollIntervalSec: &short}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	_, err = client.UpdateSubscription(ctx, connect.NewRequest(&feedpb.UpdateSubscriptionRequest{Id: id, FolderId: &missing}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = client.UpdateSubscription(ctx, connect.NewRequest(&feedpb.UpdateSubscriptionRequest{Id: missing}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = client.RefreshSubscription(ctx, connect.NewRequest(&feedpb.RefreshSubscriptionRequest{Id: id}))
	require.NoError(t, err)
	_, err = client.RefreshSubscription(ctx, connect.NewRequest(&feedpb.RefreshSubscriptionRequest{Id: missing}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	for range 2 { // deleting what is gone is not an error
		_, err = client.DeleteSubscription(ctx, connect.NewRequest(&feedpb.DeleteSubscriptionRequest{Id: id}))
		require.NoError(t, err)
	}
	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	assert.Equal(t, 0, len(subs))
}

func TestFoldersOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	_, err := client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Name: "  "}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	a, err := client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Name: "A"}))
	require.NoError(t, err)
	b, err := client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Name: "B"}))
	require.NoError(t, err)

	_, err = client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Id: b.Msg.Id, Name: "A"}))
	assert.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	_, err = client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Id: 999, Name: "C"}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	renamed, err := client.SaveFolder(ctx, connect.NewRequest(&feedpb.Folder{Id: b.Msg.Id, Name: "C"}))
	require.NoError(t, err)
	assert.Equal(t, "C", renamed.Msg.Name)

	_, err = client.DeleteFolder(ctx, connect.NewRequest(&feedpb.DeleteFolderRequest{Id: a.Msg.Id}))
	require.NoError(t, err)
	folders, err := store.ListFolders()
	require.NoError(t, err)
	require.Equal(t, 1, len(folders))
	assert.Equal(t, "C", folders[0].Name)
}

func TestFiltersOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	for _, bad := range []*feedpb.Filter{
		{Query: "type:audio", Action: feedpb.Filter_ACTION_TAG},
		{Query: "type:audio", Action: feedpb.Filter_ACTION_UNSPECIFIED},
		{Query: "(unclosed", Action: feedpb.Filter_ACTION_SKIP},
	} {
		_, err := client.SaveFilter(ctx, connect.NewRequest(bad))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), bad.String())
	}

	tag, err := client.SaveFilter(ctx, connect.NewRequest(&feedpb.Filter{SortOrder: 2, Query: "type:audio", Action: feedpb.Filter_ACTION_TAG, ActionValue: " pod "}))
	require.NoError(t, err)
	assert.Equal(t, "pod", tag.Msg.ActionValue)
	all, err := client.SaveFilter(ctx, connect.NewRequest(&feedpb.Filter{SortOrder: 1, Action: feedpb.Filter_ACTION_DOWNLOAD}))
	require.NoError(t, err)
	assert.Equal(t, "*", all.Msg.Query)

	tag.Msg.Action = feedpb.Filter_ACTION_SKIP
	_, err = client.SaveFilter(ctx, connect.NewRequest(tag.Msg))
	require.NoError(t, err)
	_, err = client.SaveFilter(ctx, connect.NewRequest(&feedpb.Filter{Id: 999, Query: "*", Action: feedpb.Filter_ACTION_SKIP}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	list, err := client.ListFilters(ctx, connect.NewRequest(&feedpb.ListFiltersRequest{}))
	require.NoError(t, err)
	require.Equal(t, 2, len(list.Msg.Filters))
	assert.Equal(t, all.Msg.Id, list.Msg.Filters[0].Id)
	assert.Equal(t, feedpb.Filter_ACTION_SKIP, list.Msg.Filters[1].Action)

	_, err = client.DeleteFilter(ctx, connect.NewRequest(&feedpb.DeleteFilterRequest{Id: all.Msg.Id}))
	require.NoError(t, err)
	list, err = client.ListFilters(ctx, connect.NewRequest(&feedpb.ListFiltersRequest{}))
	require.NoError(t, err)
	assert.Equal(t, 1, len(list.Msg.Filters))
}

func TestImportOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()
	missing := int64(999)

	_, err := client.Import(ctx, connect.NewRequest(&feedpb.ImportRequest{Data: []byte(sampleOPML), FolderId: &missing}))
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	_, err = client.Import(ctx, connect.NewRequest(&feedpb.ImportRequest{Format: feedpb.ImportRequest_FORMAT_NEWPIPE, Data: []byte("not a zip")}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	res, err := client.Import(ctx, connect.NewRequest(&feedpb.ImportRequest{Data: []byte(sampleOPML)}))
	require.NoError(t, err)
	assert.Equal(t, int64(3), res.Msg.Added)
	res, err = client.Import(ctx, connect.NewRequest(&feedpb.ImportRequest{Format: feedpb.ImportRequest_FORMAT_OPML, Data: []byte(sampleOPML)}))
	require.NoError(t, err)
	assert.Equal(t, int64(0), res.Msg.Added)
	assert.Equal(t, int64(3), res.Msg.Skipped)

	subs, err := store.ListSubscriptions()
	require.NoError(t, err)
	assert.Equal(t, 3, len(subs))
}
