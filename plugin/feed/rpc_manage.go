package feed

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/mattn/go-sqlite3"

	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/feedpb"
)

// What a client may send for the management RPCs, as the web's forms bound it.
const (
	maxNameLen        = 200
	minPollInterval   = 60
	maxImportBytes    = 20 << 20
	maxActionValueLen = 200
)

// notFound answers NotFound for a missing row, and passes any other error on.
func notFound(err error, what string, id int64) error {
	if errors.Is(err, sql.ErrNoRows) {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s %d not found", what, id))
	}
	return err
}

// isUnique reports whether err is a UNIQUE constraint failing.
func isUnique(err error) bool {
	se, ok := errors.AsType[sqlite3.Error](err)
	return ok && se.ExtendedCode == sqlite3.ErrConstraintUnique
}

// requiredName trims a name a client sent and checks it is there and not too long.
func requiredName(field, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is required", field))
	case utf8.RuneCountInString(s) > maxNameLen:
		return "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s is longer than %d characters", field, maxNameLen))
	}
	return s, nil
}

// folderRef is the folder a request names: unset or 0 is none. A folder that
// is not there is NotFound, so a client learns that rather than a constraint
// failing.
func folderRef(store *Store, id *int64) (sql.NullInt64, error) {
	if id == nil || *id == 0 {
		return sql.NullInt64{}, nil
	}
	if _, err := store.GetFolder(*id); err != nil {
		return sql.NullInt64{}, notFound(err, "folder", *id)
	}
	return sql.NullInt64{Int64: *id, Valid: true}, nil
}

// subscriptionWithUnread returns a subscription as a client sees it, with its
// unread count.
func (s *FeedService) subscriptionWithUnread(store *Store, id int64) (*feedpb.Subscription, error) {
	sub, err := store.GetSubscription(id)
	if err != nil {
		return nil, notFound(err, "subscription", id)
	}
	pb, err := subscriptionPB(sub)
	if err != nil {
		return nil, err
	}
	pb.Unread, err = store.CountView(ViewSpec{SubscriptionID: sql.NullInt64{Int64: id, Valid: true}, HideRead: true}, nil, s.plugin.ctx.Now())
	return pb, err
}

// AddSubscription subscribes to a feed, channel or page, detecting which as
// the web does, and starts fetching it.
func (s *FeedService) AddSubscription(ctx context.Context, req *connect.Request[feedpb.AddSubscriptionRequest]) (*connect.Response[feedpb.Subscription], error) {
	rawURL := strings.TrimSpace(req.Msg.Url)
	if err := CheckURL(rawURL); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("url must be a web address starting with http:// or https://"))
	}
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	folder, err := folderRef(store, req.Msg.FolderId)
	if err != nil {
		return nil, err
	}
	id, err := store.CreateSubscription(Subscription{Kind: DetectKind(ctx, rawURL), URL: rawURL, FolderID: folder})
	if isUnique(err) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("already subscribed to %s", rawURL))
	}
	if err != nil {
		return nil, err
	}
	s.plugin.TriggerFetch(plugin.UserFromContext(ctx).Username, id)
	pb, err := s.subscriptionWithUnread(store, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(pb), nil
}

// UpdateSubscription changes the fields a request sets, all at once.
func (s *FeedService) UpdateSubscription(ctx context.Context, req *connect.Request[feedpb.UpdateSubscriptionRequest]) (*connect.Response[feedpb.Subscription], error) {
	m := req.Msg
	var c SubscriptionChanges
	if m.Title != nil {
		title := strings.TrimSpace(*m.Title)
		if utf8.RuneCountInString(title) > maxNameLen {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("title is longer than %d characters", maxNameLen))
		}
		c.Title = &title
	}
	if m.PollIntervalSec != nil {
		if *m.PollIntervalSec < minPollInterval {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("poll_interval_sec must be at least %d", minPollInterval))
		}
		c.PollIntervalSec = m.PollIntervalSec
	}
	c.Paused = m.Paused
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if m.FolderId != nil {
		folder, err := folderRef(store, m.FolderId)
		if err != nil {
			return nil, err
		}
		c.Folder = &folder
	}
	if err := store.UpdateSubscription(m.Id, c); err != nil {
		return nil, notFound(err, "subscription", m.Id)
	}
	if c.Title != nil && *c.Title == "" {
		s.plugin.TriggerFetch(plugin.UserFromContext(ctx).Username, m.Id)
	}
	pb, err := s.subscriptionWithUnread(store, m.Id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(pb), nil
}

// DeleteSubscription unsubscribes, deleting its items. Like DeletePlaylist,
// deleting one that is not there is not an error.
func (s *FeedService) DeleteSubscription(ctx context.Context, req *connect.Request[feedpb.DeleteSubscriptionRequest]) (*connect.Response[feedpb.DeleteResponse], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.DeleteSubscription(req.Msg.Id); err != nil {
		return nil, err
	}
	// Its downloads count toward the user's quota; have the orchestrator
	// collect them now rather than on its next tick.
	s.plugin.TriggerFetch(plugin.UserFromContext(ctx).Username, 0)
	return connect.NewResponse(&feedpb.DeleteResponse{}), nil
}

// RefreshSubscription fetches a subscription now rather than when it is due.
// It answers before the fetch is done; the client sees what it brought in
// through its next sync.
func (s *FeedService) RefreshSubscription(ctx context.Context, req *connect.Request[feedpb.RefreshSubscriptionRequest]) (*connect.Response[feedpb.Subscription], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	id := req.Msg.Id
	if _, err := store.GetSubscription(id); err != nil {
		return nil, notFound(err, "subscription", id)
	}
	if err := store.UpdateSubscriptionNextCheck(id, 1); err != nil {
		return nil, err
	}
	s.plugin.TriggerFetch(plugin.UserFromContext(ctx).Username, id)
	pb, err := s.subscriptionWithUnread(store, id)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(pb), nil
}

// SaveFolder creates a folder (id 0) or renames one. The name is trimmed and
// required, and must be unused.
func (s *FeedService) SaveFolder(ctx context.Context, req *connect.Request[feedpb.Folder]) (*connect.Response[feedpb.Folder], error) {
	n, err := requiredName("name", req.Msg.Name)
	if err != nil {
		return nil, err
	}
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	id := req.Msg.Id
	if id == 0 {
		id, err = store.CreateFolder(n)
	} else {
		err = store.RenameFolder(id, n)
	}
	if isUnique(err) {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("a folder is already named %q", n))
	}
	if err != nil {
		return nil, notFound(err, "folder", id)
	}
	unread, err := store.CountView(ViewSpec{FolderID: sql.NullInt64{Int64: id, Valid: true}, HideRead: true}, nil, s.plugin.ctx.Now())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&feedpb.Folder{Id: id, Name: n, Unread: unread}), nil
}

// DeleteFolder removes a folder; its subscriptions stay, in no folder.
// Deleting one that is not there is not an error.
func (s *FeedService) DeleteFolder(ctx context.Context, req *connect.Request[feedpb.DeleteFolderRequest]) (*connect.Response[feedpb.DeleteResponse], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.DeleteFolder(req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&feedpb.DeleteResponse{}), nil
}

var filterActions = map[string]feedpb.Filter_Action{
	"skip":           feedpb.Filter_ACTION_SKIP,
	"tag":            feedpb.Filter_ACTION_TAG,
	"download":       feedpb.Filter_ACTION_DOWNLOAD,
	"mute_profanity": feedpb.Filter_ACTION_MUTE_PROFANITY,
	"transcribe":     feedpb.Filter_ACTION_TRANSCRIBE,
}

func filterPB(f Filter) (*feedpb.Filter, error) {
	action, ok := filterActions[f.Action]
	if !ok {
		return nil, fmt.Errorf("filter %d: unknown action %q", f.ID, f.Action)
	}
	return &feedpb.Filter{Id: f.ID, SortOrder: f.SortOrder, Query: f.Query, Action: action, ActionValue: f.ActionValue}, nil
}

// ListFilters returns the filters in the order they apply.
func (s *FeedService) ListFilters(ctx context.Context, _ *connect.Request[feedpb.ListFiltersRequest]) (*connect.Response[feedpb.ListFiltersResponse], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	filters, err := store.ListFilters()
	if err != nil {
		return nil, err
	}
	res := &feedpb.ListFiltersResponse{Filters: make([]*feedpb.Filter, len(filters))}
	for i, f := range filters {
		if res.Filters[i], err = filterPB(f); err != nil {
			return nil, err
		}
	}
	return connect.NewResponse(res), nil
}

// SaveFilter creates a filter (id 0) or replaces one. An empty query matches
// everything, as on the web; a tag filter needs the tag.
func (s *FeedService) SaveFilter(ctx context.Context, req *connect.Request[feedpb.Filter]) (*connect.Response[feedpb.Filter], error) {
	m := req.Msg
	f := Filter{ID: m.Id, SortOrder: m.SortOrder, Query: strings.TrimSpace(m.Query), ActionValue: strings.TrimSpace(m.ActionValue)}
	if f.Query == "" {
		f.Query = "*"
	}
	if len(f.Query) > maxQueryLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("query is longer than %d bytes", maxQueryLen))
	}
	if _, err := ParseFilterQuery(f.Query); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("query: %w", err))
	}
	for a, pb := range filterActions {
		if pb == m.Action {
			f.Action = a
		}
	}
	switch {
	case f.Action == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown action %v", m.Action))
	case f.Action == "tag" && f.ActionValue == "":
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a tag filter needs the tag in action_value"))
	case utf8.RuneCountInString(f.ActionValue) > maxActionValueLen:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("action_value is longer than %d characters", maxActionValueLen))
	}
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if f.ID == 0 {
		f.ID, err = store.CreateFilter(f)
	} else {
		err = store.UpdateFilter(f)
	}
	if err != nil {
		return nil, notFound(err, "filter", f.ID)
	}
	pb, err := filterPB(f)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(pb), nil
}

// DeleteFilter removes a filter. Deleting one that is not there is not an
// error.
func (s *FeedService) DeleteFilter(ctx context.Context, req *connect.Request[feedpb.DeleteFilterRequest]) (*connect.Response[feedpb.DeleteResponse], error) {
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	if err := store.DeleteFilter(req.Msg.Id); err != nil {
		return nil, err
	}
	return connect.NewResponse(&feedpb.DeleteResponse{}), nil
}

var importFormats = map[feedpb.ImportRequest_Format]ImportFormat{
	feedpb.ImportRequest_FORMAT_UNSPECIFIED: "",
	feedpb.ImportRequest_FORMAT_OPML:        ImportOPML,
	feedpb.ImportRequest_FORMAT_ANTENNAPOD:  ImportAntennaPod,
	feedpb.ImportRequest_FORMAT_NEWPIPE:     ImportNewPipe,
	feedpb.ImportRequest_FORMAT_YOUTUBE_CSV: ImportYouTubeCSV,
}

// Import adds the subscriptions in another app's export, all or none, and
// starts fetching them.
func (s *FeedService) Import(ctx context.Context, req *connect.Request[feedpb.ImportRequest]) (*connect.Response[feedpb.ImportResponse], error) {
	m := req.Msg
	format, ok := importFormats[m.Format]
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown format %v", m.Format))
	}
	if len(m.Data) > maxImportBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("data is larger than %d bytes", maxImportBytes))
	}
	store, err := s.store(ctx)
	if err != nil {
		return nil, err
	}
	folder, err := folderRef(store, m.FolderId)
	if err != nil {
		return nil, err
	}
	res, err := store.Import(format, m.Data, folder)
	if errors.Is(err, ErrUnreadableImport) {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err != nil {
		return nil, err
	}
	if res.Added > 0 {
		s.plugin.TriggerFetch(plugin.UserFromContext(ctx).Username, 0)
	}
	return connect.NewResponse(&feedpb.ImportResponse{Added: int64(res.Added), Skipped: int64(res.Skipped)}), nil
}
