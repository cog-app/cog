package feed

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb/feedpbconnect"
)

// FeedService serves native clients the views, item state and management the
// web UI has. An RPC it does not implement yet answers Unimplemented.
type FeedService struct {
	feedpbconnect.UnimplementedFeedServiceHandler
	plugin *Plugin
}

// registerService mounts FeedService at /cogfeed.v1.FeedService/.
func (p *Plugin) registerService(ctx plugin.PluginContext) {
	path, h := feedpbconnect.NewFeedServiceHandler(&FeedService{plugin: p},
		connect.WithInterceptors(serverErrors{p}))
	ctx.RegisterService(path, h)
}

// serverErrors logs an error an RPC returns that is not a connect.Error —
// a fault of the server's, not an answer to the client — and sends the
// client a generic Internal in its place, so a database path or query never
// reaches it.
type serverErrors struct{ plugin *Plugin }

func (e serverErrors) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		res, err := next(ctx, req)
		return res, e.check(req.Spec().Procedure, err)
	}
}

func (e serverErrors) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (e serverErrors) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		return e.check(conn.Spec().Procedure, next(ctx, conn))
	}
}

func (e serverErrors) check(procedure string, err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return err
	}
	e.plugin.log.Error("feed: rpc", "procedure", procedure, "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// store opens the calling user's feed database.
func (s *FeedService) store(ctx context.Context) (*Store, error) {
	u := plugin.UserFromContext(ctx)
	if u == nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))
	}
	db, err := s.plugin.DB(u.Username)
	if err != nil {
		return nil, err
	}
	return NewStore(db), nil
}

// GetLibrary returns what a client's navigation shows: folders,
// subscriptions, playlists and tags with their counts.
func (s *FeedService) GetLibrary(ctx context.Context, _ *connect.Request[feedpb.GetLibraryRequest]) (*connect.Response[feedpb.Library], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	folders, err := store.ListFolders()
	if err != nil {
		return nil, err
	}
	subs, err := store.ListSubscriptions()
	if err != nil {
		return nil, err
	}
	playlists, err := store.ListPlaylists()
	if err != nil {
		return nil, err
	}
	tags, err := store.ListTagCounts()
	if err != nil {
		return nil, err
	}
	counts, err := store.LoadSidebarCounts()
	if err != nil {
		return nil, err
	}

	lib := &feedpb.Library{
		Counts: &feedpb.Counts{
			All:        int64(counts.TotalItems),
			Unread:     int64(counts.TotalUnread),
			Starred:    int64(counts.StarredCount),
			Downloaded: int64(counts.Downloaded),
		},
	}
	for _, f := range folders {
		lib.Folders = append(lib.Folders, &feedpb.Folder{Id: f.ID, Name: f.Name, Unread: int64(counts.ByFolder[f.ID])})
	}
	for _, sub := range subs {
		pb, err := subscriptionPB(sub)
		if err != nil {
			return nil, err
		}
		pb.Unread = int64(counts.BySubscription[sub.ID])
		lib.Subscriptions = append(lib.Subscriptions, pb)
	}
	for _, pl := range playlists {
		lib.Playlists = append(lib.Playlists, &feedpb.Playlist{Id: pl.ID, Name: pl.Name, Query: pl.Query, SortOrder: pl.SortOrder})
	}
	for _, t := range tags {
		lib.Tags = append(lib.Tags, &feedpb.TagCount{Name: t.Name, Count: t.Count})
	}
	return connect.NewResponse(lib), nil
}

var subscriptionKinds = map[string]feedpb.SubscriptionKind{
	"rss":     feedpb.SubscriptionKind_SUBSCRIPTION_KIND_RSS,
	"youtube": feedpb.SubscriptionKind_SUBSCRIPTION_KIND_YOUTUBE,
	"url":     feedpb.SubscriptionKind_SUBSCRIPTION_KIND_URL,
}

func subscriptionPB(sub Subscription) (*feedpb.Subscription, error) {
	kind, ok := subscriptionKinds[sub.Kind]
	if !ok {
		return nil, fmt.Errorf("subscription %d: unknown kind %q", sub.ID, sub.Kind)
	}
	pb := &feedpb.Subscription{
		Id:              sub.ID,
		Kind:            kind,
		Url:             sub.URL,
		Title:           sub.Title,
		Description:     sub.Description,
		SiteUrl:         sub.SiteURL,
		ImageUrl:        sub.ImageURL,
		PollIntervalSec: sub.PollIntervalSec,
		Paused:          sub.Paused,
		LastError:       sub.LastError,
		LastSuccessAt:   unixPB(sub.LastSuccessAt),
	}
	if sub.FolderID.Valid {
		pb.FolderId = &sub.FolderID.Int64
	}
	return pb, nil
}

// unixPB is a Unix time as a Timestamp, or nil for zero (never).
func unixPB(sec int64) *timestamppb.Timestamp {
	if sec == 0 {
		return nil
	}
	return &timestamppb.Timestamp{Seconds: sec}
}
