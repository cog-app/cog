package feed_test

import (
	"context"
	"database/sql"
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		RequestDownload(context.Background(), connect.NewRequest(&feedpb.RequestDownloadRequest{}))
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

// TestGetItemContentAsMarkdown checks an article body reaches a client as
// Markdown too, with relative links made absolute against the item's page,
// and an item with no body has none.
func TestGetItemContentAsMarkdown(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	subID, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://blog.example.com/feed", Title: "Blog"})
	require.NoError(t, err)
	article, _, err := store.UpsertItem(feed.Item{
		SubscriptionID: subID,
		GUID:           "post",
		Type:           "text",
		Title:          "Post",
		URL:            "https://blog.example.com/2026/post",
		ContentHTML:    `<h2>Intro</h2><p>Read <a href="/about">this</a> and <strong>that</strong>.</p><ul><li>one</li><li>two</li></ul><img src="pic.png" alt="pic">`,
	})
	require.NoError(t, err)
	empty := seedAudioItem(t, store, "https://cdn.example.com/ep1.mp3")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	got, err := client.GetItem(ctx, connect.NewRequest(&feedpb.GetItemRequest{Id: article}))
	require.NoError(t, err)
	assert.Equal(t, "## Intro\n\nRead [this](https://blog.example.com/about) and **that**.\n\n- one\n- two\n\n![pic](https://blog.example.com/2026/pic.png)", got.Msg.ContentMarkdown)

	got, err = client.GetItem(ctx, connect.NewRequest(&feedpb.GetItemRequest{Id: empty}))
	require.NoError(t, err)
	assert.Equal(t, "", got.Msg.ContentMarkdown)
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

func TestPreviewQueryOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	ids, video, _ := playlistItems(t, store)
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	preview := func(q string, sample int32) *feedpb.PreviewQueryResponse {
		t.Helper()
		res, err := client.PreviewQuery(context.Background(), connect.NewRequest(&feedpb.PreviewQueryRequest{Query: q, Sample: sample}))
		require.NoError(t, err)
		return res.Msg
	}

	got := preview("type:audio", 2)
	assert.Equal(t, "", got.Error)
	assert.Equal(t, int64(4), got.Count)
	require.Equal(t, 2, len(got.Sample))
	assert.Equal(t, ids[3], got.Sample[0].Id) // newest first

	// A text search goes through the refine step.
	got = preview("ep4", 0)
	assert.Equal(t, int64(1), got.Count)
	require.Equal(t, 1, len(got.Sample))
	assert.Equal(t, video, got.Sample[0].Id)

	got = preview("(type:audio", 0)
	assert.True(t, got.Error != "", "no error")
	require.True(t, got.ErrorPosition != nil, "no position")
	assert.Equal(t, int32(11), got.GetErrorPosition())
	assert.Equal(t, int64(0), got.Count)

	// A comparison the field cannot do parses but is still the client's to fix.
	got = preview("type>3", 0)
	assert.True(t, got.Error != "", "no error for a range on a keyword")
	assert.True(t, got.ErrorPosition == nil, "a compile error has no position")

	got = preview("  ", 0)
	assert.True(t, got.Error == "" && got.Count == 0 && len(got.Sample) == 0, "an empty query previews nothing")

	_, err := client.PreviewQuery(context.Background(), connect.NewRequest(&feedpb.PreviewQueryRequest{Query: strings.Repeat("a", 1001)}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestPlayerPrefsOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()

	res, err := client.GetPlayerPrefs(ctx, connect.NewRequest(&feedpb.GetPlayerPrefsRequest{}))
	require.NoError(t, err)
	prefs := res.Msg
	assert.Equal(t, 1.0, prefs.Speed)
	assert.Equal(t, int32(15), prefs.SkipBackSec)
	assert.True(t, prefs.AutoplayNext)

	prefs.Speed, prefs.SkipFwdSec, prefs.SkipSilence = 1.5, 45, true
	_, err = client.SetPlayerPrefs(ctx, connect.NewRequest(prefs))
	require.NoError(t, err)
	res, err = client.GetPlayerPrefs(ctx, connect.NewRequest(&feedpb.GetPlayerPrefsRequest{}))
	require.NoError(t, err)
	assert.Equal(t, 1.5, res.Msg.Speed)
	assert.Equal(t, int32(45), res.Msg.SkipFwdSec)
	assert.True(t, res.Msg.SkipSilence)

	stored, err := feedStore(t, env, u.Username).LoadPlayerPrefs()
	require.NoError(t, err)
	assert.Equal(t, 1.5, stored.Speed)

	// Prefs a client did not fill in are refused, not stored as zeros.
	for _, bad := range []*feedpb.PlayerPrefs{
		{Speed: 0, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1},
		{Speed: 1, SkipBackSec: 0, SkipFwdSec: 30, Volume: 1},
		{Speed: 1, SkipBackSec: 15, SkipFwdSec: 121, Volume: 1},
		{Speed: 1, SkipBackSec: 15, SkipFwdSec: 30, Volume: 1.5},
	} {
		_, err := client.SetPlayerPrefs(ctx, connect.NewRequest(bad))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%+v", bad)
	}
}

func TestSetPlayerPrefsSkipSilenceMin(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	client := feedClient(t, env, u.AppPassword(t, "phone"))
	ctx := context.Background()
	get := func() *feedpb.PlayerPrefs {
		t.Helper()
		res, err := client.GetPlayerPrefs(ctx, connect.NewRequest(&feedpb.GetPlayerPrefsRequest{}))
		require.NoError(t, err)
		return res.Msg
	}

	prefs := get()
	assert.Equal(t, 1.0, prefs.SkipSilenceMinSec)

	prefs.SkipSilenceMinSec = 2.5
	_, err := client.SetPlayerPrefs(ctx, connect.NewRequest(prefs))
	require.NoError(t, err)
	assert.Equal(t, 2.5, get().SkipSilenceMinSec)

	// Zero is a client that predates the field: the stored value stays.
	prefs.SkipSilenceMinSec = 0
	prefs.Speed = 1.25
	_, err = client.SetPlayerPrefs(ctx, connect.NewRequest(prefs))
	require.NoError(t, err)
	after := get()
	assert.Equal(t, 2.5, after.SkipSilenceMinSec)
	assert.Equal(t, 1.25, after.Speed)

	for _, bad := range []float64{0.4, 10.5, -1, math.NaN(), math.Inf(1)} {
		prefs.SkipSilenceMinSec = bad
		_, err := client.SetPlayerPrefs(ctx, connect.NewRequest(prefs))
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%g", bad)
	}
	assert.Equal(t, 2.5, get().SkipSilenceMinSec)
}

// Changing the shortest silence changes the plan, so the edges are remade.
func TestSetPlayerPrefsResetsEdges(t *testing.T) {
	t.Skip("Task 6")
}

func TestExportOPMLOverGRPC(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	store := feedStore(t, env, u.Username)
	_, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: "https://pod.example.com/feed", Title: "Pod"})
	require.NoError(t, err)
	res, err := feedClient(t, env, u.AppPassword(t, "phone")).
		ExportOPML(context.Background(), connect.NewRequest(&feedpb.ExportOPMLRequest{}))
	require.NoError(t, err)
	want, err := store.ExportOPML()
	require.NoError(t, err)
	assert.Equal(t, string(want), string(res.Msg.Opml))
	assert.True(t, strings.Contains(string(res.Msg.Opml), "https://pod.example.com/feed"), "feed missing from the OPML")
}

// A phone signs in with Login Flow v2 and calls the service with the app
// password it collects, as a Bearer token or with Basic auth; the password
// is named after the app and the device.
func TestLoginFlowAppPasswordCallsFeedService(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "alice", "pass")
	const ua = "CogFeed/0.1 (Pixel 8; Android 15)"

	var start struct {
		Poll struct {
			Token string `json:"token"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	env.Post(t, "/index.php/login/v2", cogtest.Header("User-Agent", ua)).Status(http.StatusOK).JSON(&start)
	login, err := url.Parse(start.Login)
	require.NoError(t, err)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	env.PostForm(t, "/index.php/login/v2/grant?token="+url.QueryEscape(login.Query().Get("token")),
		url.Values{"csrf_token": {auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)}},
		cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID),
	).Status(http.StatusOK).Contains("Access granted")

	var collected struct {
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {start.Poll.Token}}).
		Status(http.StatusOK).JSON(&collected)
	require.Equal(t, "alice", collected.LoginName)

	pws, err := env.Providers.AppPasswords.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(pws))
	assert.True(t, strings.HasPrefix(pws[0].Name, "CogFeed on Pixel 8 ("), "app password named %q", pws[0].Name)

	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte(collected.LoginName+":"+collected.AppPassword))
	for _, authorization := range []string{"Bearer " + collected.AppPassword, basic} {
		_, err := feedClient(t, env, authorization).
			GetLibrary(context.Background(), connect.NewRequest(&feedpb.GetLibraryRequest{}))
		assert.NoError(t, err)
	}
}
