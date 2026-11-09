package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/mattn/go-sqlite3"
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
	plCounts, err := store.PlaylistCounts(playlists, s.plugin.ctx.Now(), feedViewLimit)
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
		lib.Playlists = append(lib.Playlists, playlistPB(pl, plCounts[pl.ID]))
	}
	for _, t := range tags {
		lib.Tags = append(lib.Tags, &feedpb.TagCount{Name: t.Name, Count: t.Count})
	}
	return connect.NewResponse(lib), nil
}

var playOrders = map[PlayOrder]feedpb.PlayOrder{
	OrderNewest:   feedpb.PlayOrder_PLAY_ORDER_NEWEST,
	OrderOldest:   feedpb.PlayOrder_PLAY_ORDER_OLDEST,
	OrderShortest: feedpb.PlayOrder_PLAY_ORDER_SHORTEST,
	OrderShuffled: feedpb.PlayOrder_PLAY_ORDER_SHUFFLED,
}

func playlistPB(pl Playlist, c PlaylistCounts) *feedpb.Playlist {
	return &feedpb.Playlist{
		Id:                    pl.ID,
		Name:                  pl.Name,
		Query:                 pl.Query,
		SortOrder:             pl.SortOrder,
		Count:                 c.Items,
		UnfinishedCount:       c.Unfinished,
		UnfinishedDurationSec: c.UnfinishedSec,
		Order:                 playOrders[pl.Order],
		ShuffleSeed:           pl.ShuffleSeed,
		ListenOnly:            pl.ListenOnly,
	}
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

// getItem loads an item, or answers NotFound.
func getItem(store *Store, id int64) (Item, error) {
	it, err := store.GetItem(id)
	if errors.Is(err, sql.ErrNoRows) {
		return Item{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("item %d not found", id))
	}
	return it, err
}

// itemPB converts one item as a client sees it, and returns its download.
func itemPB(store *Store, it Item) (*feedpb.Item, Download, error) {
	sub, err := store.GetSubscription(it.SubscriptionID)
	if err != nil {
		return nil, Download{}, err
	}
	pbs, downloads, err := itemsPB(store, []ItemWithSubscription{{Item: it, SubscriptionTitle: sub.Title}})
	if err != nil {
		return nil, Download{}, err
	}
	return pbs[0], downloads[it.ID], nil
}

// itemsPB converts items as a client sees them, loading their tags,
// playback and downloads in one query each. It also returns the downloads,
// for what an RPC tells beyond the Item.
func itemsPB(store *Store, items []ItemWithSubscription) ([]*feedpb.Item, map[int64]Download, error) {
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	tags, err := store.TagsByItems(ids)
	if err != nil {
		return nil, nil, err
	}
	playback, err := store.PlaybackByItems(ids)
	if err != nil {
		return nil, nil, err
	}
	downloads, err := store.DownloadsByItems(ids)
	if err != nil {
		return nil, nil, err
	}
	out := make([]*feedpb.Item, len(items))
	for i, it := range items {
		typ, ok := itemTypes[it.Type]
		if !ok {
			return nil, nil, fmt.Errorf("item %d: unknown type %q", it.ID, it.Type)
		}
		pb := &feedpb.Item{
			Id:                it.ID,
			SubscriptionId:    it.SubscriptionID,
			SubscriptionTitle: it.SubscriptionTitle,
			Type:              typ,
			Title:             it.Title,
			Author:            it.Author,
			Url:               it.URL,
			ThumbnailUrl:      it.ThumbnailURL,
			DurationSec:       it.DurationSec,
			PublishedAt:       unixPB(it.PublishedAt),
			Read:              it.IsRead,
			Starred:           it.IsStarred,
			Tags:              tags[it.ID],
		}
		if p, ok := playback[it.ID]; ok {
			pb.Playback = playbackPB(p)
		}
		if d, ok := downloads[it.ID]; ok {
			pb.ServerDownload = &feedpb.DownloadState{State: downloadStates[d.State], Error: d.Error, Bytes: d.Bytes}
		}
		out[i] = pb
	}
	return out, downloads, nil
}

var itemTypes = map[string]feedpb.ItemType{
	"text":  feedpb.ItemType_ITEM_TYPE_TEXT,
	"audio": feedpb.ItemType_ITEM_TYPE_AUDIO,
	"video": feedpb.ItemType_ITEM_TYPE_VIDEO,
}

var downloadStates = map[string]feedpb.DownloadState_State{
	"queued":      feedpb.DownloadState_STATE_QUEUED,
	"downloading": feedpb.DownloadState_STATE_DOWNLOADING,
	"ready":       feedpb.DownloadState_STATE_READY,
	"error":       feedpb.DownloadState_STATE_ERROR,
}

func playbackPB(p Playback) *feedpb.Playback {
	return &feedpb.Playback{ItemId: p.ItemID, ProgressSec: p.ProgressSec, Completed: p.Completed, At: unixPB(p.LastPlayedAt)}
}

// GetItem returns an item with its article body.
func (s *FeedService) GetItem(ctx context.Context, req *connect.Request[feedpb.GetItemRequest]) (*connect.Response[feedpb.ItemDetail], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	it, err := getItem(store, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	pb, d, err := itemPB(store, it)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&feedpb.ItemDetail{
		Item:        pb,
		ContentHtml: it.ContentHTML,
		HasCaptions: d.CaptionsPath != "",
	}), nil
}

// changeItem checks the item exists, applies change, and returns the item
// as it is after.
func (s *FeedService) changeItem(ctx context.Context, id int64, change func(*Store) error) (*connect.Response[feedpb.Item], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := getItem(store, id); err != nil {
		return nil, err
	}
	if err := change(store); err != nil {
		return nil, err
	}
	it, err := getItem(store, id)
	if err != nil {
		return nil, err
	}
	pb, _, err := itemPB(store, it)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(pb), nil
}

func (s *FeedService) SetRead(ctx context.Context, req *connect.Request[feedpb.SetReadRequest]) (*connect.Response[feedpb.Item], error) {
	return s.changeItem(ctx, req.Msg.Id, func(store *Store) error {
		return store.MarkRead(req.Msg.Id, req.Msg.Read)
	})
}

func (s *FeedService) SetStarred(ctx context.Context, req *connect.Request[feedpb.SetStarredRequest]) (*connect.Response[feedpb.Item], error) {
	return s.changeItem(ctx, req.Msg.Id, func(store *Store) error {
		return store.MarkStarred(req.Msg.Id, req.Msg.Starred)
	})
}

// SetTags adds and removes tags on an item. Names are trimmed, and an empty
// one is refused, as the web form does.
func (s *FeedService) SetTags(ctx context.Context, req *connect.Request[feedpb.SetTagsRequest]) (*connect.Response[feedpb.Item], error) {
	add, err := tagNames(req.Msg.Add)
	if err != nil {
		return nil, err
	}
	remove, err := tagNames(req.Msg.Remove)
	if err != nil {
		return nil, err
	}
	return s.changeItem(ctx, req.Msg.Id, func(store *Store) error {
		for _, name := range add {
			if err := store.AddTag(req.Msg.Id, name); err != nil {
				return err
			}
		}
		for _, name := range remove {
			if err := store.RemoveTag(req.Msg.Id, name); err != nil {
				return err
			}
		}
		return nil
	})
}

func tagNames(names []string) ([]string, error) {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.TrimSpace(n)
		if out[i] == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("tag name is required"))
		}
	}
	return out, nil
}

// ReportProgress records where a client is in an item, as of when it
// played there; see Store.SetPlayback. A client with no clock reading sends
// no at, and is taken as playing now. Completing an item marks it read, as
// finishing it in the web player does.
func (s *FeedService) ReportProgress(ctx context.Context, req *connect.Request[feedpb.ReportProgressRequest]) (*connect.Response[feedpb.Playback], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	id := req.Msg.ItemId
	if _, err := getItem(store, id); err != nil {
		return nil, err
	}
	at := time.Now()
	if req.Msg.At != nil {
		if err := req.Msg.At.CheckValid(); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at: %w", err))
		}
		at = req.Msg.At.AsTime()
	}
	if err := store.SetPlayback(id, req.Msg.ProgressSec, req.Msg.Completed, at); err != nil {
		return nil, err
	}
	if req.Msg.Completed {
		if err := store.MarkRead(id, true); err != nil {
			return nil, err
		}
	}
	// What is stored, which is not what was sent when a later update won.
	pb, err := store.GetPlayback(id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(playbackPB(pb)), nil
}

// SavePlaylist creates a playlist (id 0) or replaces one, and returns it with
// its counts. The name is trimmed and required, and must be unused; the query
// must parse. A shuffled order with no seed gets a new one, so a client
// reshuffles by sending 0.
func (s *FeedService) SavePlaylist(ctx context.Context, req *connect.Request[feedpb.Playlist]) (*connect.Response[feedpb.Playlist], error) {
	m := req.Msg
	pl := Playlist{
		ID:          m.Id,
		Name:        strings.TrimSpace(m.Name),
		Query:       strings.TrimSpace(m.Query),
		SortOrder:   m.SortOrder,
		ShuffleSeed: m.ShuffleSeed,
		ListenOnly:  m.ListenOnly,
	}
	if pl.Name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if pl.Query == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is required"))
	}
	if _, err := ParseFilterQuery(pl.Query); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("query: %w", err))
	}
	switch m.Order {
	case feedpb.PlayOrder_PLAY_ORDER_UNSPECIFIED:
		pl.Order = OrderNewest
	default:
		for o, pb := range playOrders {
			if pb == m.Order {
				pl.Order = o
			}
		}
		if pl.Order == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown order %v", m.Order))
		}
	}
	if pl.ShuffleSeed < 0 || pl.ShuffleSeed >= 1_000_000_000 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("shuffle_seed must be in [0, 10^9)"))
	}
	if pl.Order == OrderShuffled && pl.ShuffleSeed == 0 {
		pl.ShuffleSeed = newShuffleSeed()
	}

	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if pl.ID == 0 {
		pl.ID, err = store.CreatePlaylist(pl)
	} else {
		err = store.UpdatePlaylist(pl)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("playlist %d not found", pl.ID))
	}
	if se, ok := errors.AsType[sqlite3.Error](err); ok && se.ExtendedCode == sqlite3.ErrConstraintUnique {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a playlist is already named %q", pl.Name))
	}
	if err != nil {
		return nil, err
	}
	saved, err := store.GetPlaylist(pl.ID)
	if err != nil {
		return nil, err
	}
	counts, err := store.PlaylistCounts([]Playlist{saved}, s.plugin.ctx.Now(), feedViewLimit)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(playlistPB(saved, counts[saved.ID])), nil
}

// DeletePlaylist removes a playlist. Removing one that is not there is not an
// error: the outcome is the same, and a client retrying a delete it lost the
// answer to should not see one.
func (s *FeedService) DeletePlaylist(ctx context.Context, req *connect.Request[feedpb.DeletePlaylistRequest]) (*connect.Response[feedpb.DeleteResponse], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.DeletePlaylist(req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&feedpb.DeleteResponse{}), nil
}
