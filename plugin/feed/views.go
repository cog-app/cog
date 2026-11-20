package feed

import (
	"database/sql"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/query"
	"git.duckfam.us/jj-software/cog/internal/search"
)

// PlayOrder is the order a playlist plays in.
type PlayOrder string

const (
	OrderNewest   PlayOrder = "newest"
	OrderOldest   PlayOrder = "oldest"
	OrderShortest PlayOrder = "shortest" // least time left first
	OrderShuffled PlayOrder = "shuffled"
)

// PlayOrders lists the orders in the order a picker shows them.
var PlayOrders = []PlayOrder{OrderNewest, OrderOldest, OrderShortest, OrderShuffled}

// Valid reports whether o is one of PlayOrders.
func (o PlayOrder) Valid() bool {
	return slices.Contains(PlayOrders, o)
}

// Label is how a picker names o.
func (o PlayOrder) Label() string {
	switch o {
	case OrderOldest:
		return "Oldest first"
	case OrderShortest:
		return "Shortest first"
	case OrderShuffled:
		return "Shuffled"
	default:
		return "Newest first"
	}
}

// queryNeedsRefine reports whether e contains a TextExpr/PhraseExpr — the only
// nodes the SQL compiler evaluates as a permissive superset. When false, the
// SQL WHERE is exact and no refine pass is needed.
func queryNeedsRefine(e query.Expr) bool {
	switch x := e.(type) {
	case *query.TextExpr, *query.PhraseExpr:
		return true
	case *query.NotExpr:
		return queryNeedsRefine(x.Inner)
	case *query.AndExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	case *query.OrExpr:
		return queryNeedsRefine(x.Left) || queryNeedsRefine(x.Right)
	default:
		return false
	}
}

// Refine filters the SQL-prefiltered rows down to an exact match of expr
// using bleve-exact semantics. Caller guarantees queryNeedsRefine(expr)==true.
// Builds a full query.Record per row (subscription/folder memoized by id;
// tags + download-state batch-loaded) and keeps rows where search.ExactMatch
// holds. Order preserved.
func (s *Store) Refine(items []ItemWithSubscription, expr query.Expr) ([]ItemWithSubscription, error) {
	if len(items) == 0 {
		return items, nil
	}
	ids := make([]int64, len(items))
	for i, it := range items {
		ids[i] = it.ID
	}
	tagsByItem, err := s.TagsByItems(ids)
	if err != nil {
		return nil, fmt.Errorf("refine tags: %w", err)
	}
	dlByItem, err := s.DownloadStateByItems(ids)
	if err != nil {
		return nil, fmt.Errorf("refine downloads: %w", err)
	}
	subCache := map[int64]Subscription{}
	folderCache := map[int64]string{}
	out := items[:0]
	for _, it := range items {
		sub, ok := subCache[it.SubscriptionID]
		if !ok {
			sub, err = s.GetSubscription(it.SubscriptionID)
			if err != nil {
				return nil, fmt.Errorf("refine subscription: %w", err)
			}
			subCache[it.SubscriptionID] = sub
		}
		folderName := ""
		if sub.FolderID.Valid {
			fn, ok := folderCache[sub.FolderID.Int64]
			if !ok {
				f, err := s.GetFolder(sub.FolderID.Int64)
				if err != nil {
					return nil, fmt.Errorf("refine folder: %w", err)
				}
				fn = f.Name
				folderCache[sub.FolderID.Int64] = fn
			}
			folderName = fn
		}
		rec := BuildRecord(it.Item, sub, folderName, tagsByItem[it.ID],
			!it.IsRead, it.IsStarred, dlByItem[it.ID] == "ready")
		if search.ExactMatch(expr, rec, *SearchSchema()) {
			out = append(out, it)
		}
	}
	return out, nil
}

// ListItemsMatching lists up to limit items of spec matching expr, refined
// to an exact match when the SQL alone is a superset; see ViewPage. Relative
// dates in expr are taken from now.
func (s *Store) ListItemsMatching(spec ViewSpec, expr query.Expr, now time.Time, limit int) ([]ItemWithSubscription, error) {
	items, _, err := s.ViewPage(spec, expr, now, limit)
	return items, err
}

// playlistQuery parses a playlist's query. One that no longer parses — the
// grammar changed under it — shows every item, as it always has, and is
// logged; nil means every item.
func playlistQuery(pl Playlist) query.Expr {
	if pl.Query == "" {
		return nil
	}
	e, err := query.Parse(pl.Query)
	if err != nil {
		slog.Warn("feed: playlist query does not parse; showing all items", "playlist_id", pl.ID, "query", pl.Query, "err", err)
		return nil
	}
	return e
}

// PlaylistItems lists a playlist's items in its order. spec adds to the
// playlist's own selection (HideRead, Unfinished); its view and order fields
// are the playlist's.
func (s *Store) PlaylistItems(pl Playlist, spec ViewSpec, now time.Time, limit int) ([]ItemWithSubscription, error) {
	spec.All = true
	spec.Order, spec.Seed = pl.Order, pl.ShuffleSeed
	items, err := s.ListItemsMatching(spec, playlistQuery(pl), now, limit)
	if err != nil {
		return nil, fmt.Errorf("playlist %d items: %w", pl.ID, err)
	}
	return items, nil
}

// PlaylistCounts is what a playlist's tile shows.
type PlaylistCounts struct {
	Items         int64
	Unfinished    int64 // audio and video not played to the end
	UnfinishedSec int64 // the time left to play of those
}

// PlaylistCounts counts every playlist's items. The playlists whose SQL is
// exact are counted in one query; one whose query needs the refine step
// counts what it would list (at most limit items), with one query for those
// items' playback.
func (s *Store) PlaylistCounts(playlists []Playlist, now time.Time, limit int) (map[int64]PlaylistCounts, error) {
	out := make(map[int64]PlaylistCounts, len(playlists))
	var parts []string
	var args []any
	var refine []Playlist
	for _, pl := range playlists {
		expr := playlistQuery(pl)
		if expr != nil {
			expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
			if queryNeedsRefine(expr) {
				refine = append(refine, pl)
				continue
			}
		}
		// Every item, with playback joined for the sums.
		spec := ViewSpec{All: true, Query: expr, Schema: SearchSchema(), joinPlayback: true}
		joins, where, wargs, err := spec.from()
		if err != nil {
			return nil, fmt.Errorf("playlist %d counts: %w", pl.ID, err)
		}
		q := `SELECT ? AS playlist_id, COUNT(*),
			COALESCE(SUM(CASE WHEN ` + unfinishedSQL + ` THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN ` + unfinishedSQL + ` THEN ` + leftSQL + ` ELSE 0 END), 0)
			FROM items i JOIN subscriptions s ON i.subscription_id = s.id` + joins
		if len(where) > 0 {
			q += " WHERE " + strings.Join(where, " AND ")
		}
		parts = append(parts, q)
		args = append(append(args, pl.ID), wargs...)
	}
	if len(parts) > 0 {
		rows, err := s.db.Query(strings.Join(parts, " UNION ALL "), args...)
		if err != nil {
			return nil, fmt.Errorf("playlist counts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var c PlaylistCounts
			if err := rows.Scan(&id, &c.Items, &c.Unfinished, &c.UnfinishedSec); err != nil {
				return nil, fmt.Errorf("playlist counts scan: %w", err)
			}
			out[id] = c
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("playlist counts rows: %w", err)
		}
	}
	for _, pl := range refine {
		items, err := s.PlaylistItems(pl, ViewSpec{}, now, limit)
		if err != nil {
			return nil, err
		}
		ids := make([]int64, len(items))
		for i, it := range items {
			ids[i] = it.ID
		}
		playback, err := s.PlaybackByItems(ids)
		if err != nil {
			return nil, fmt.Errorf("playlist %d counts: %w", pl.ID, err)
		}
		c := PlaylistCounts{Items: int64(len(items))}
		for _, it := range items {
			p := playback[it.ID]
			if it.Type == "text" || p.Completed {
				continue
			}
			c.Unfinished++
			c.UnfinishedSec += max(it.DurationSec-int64(p.ProgressSec), 0)
		}
		out[pl.ID] = c
	}
	return out, nil
}

// Preview counts the items expr matches and returns the first sample of
// them, newest first: the filter and playlist editors' live preview. A query
// that needs the refine step is counted over the newest limit items.
func (s *Store) Preview(expr query.Expr, now time.Time, sample, limit int) (int64, []ItemWithSubscription, error) {
	expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
	if queryNeedsRefine(expr) {
		items, err := s.ListItemsMatching(ViewSpec{}, expr, now, limit)
		if err != nil {
			return 0, nil, err
		}
		return int64(len(items)), items[:min(sample, len(items))], nil
	}
	spec := ViewSpec{All: true, Query: expr, Schema: SearchSchema()}
	joins, where, args, err := spec.from()
	if err != nil {
		return 0, nil, fmt.Errorf("preview: %w", err)
	}
	q := `SELECT COUNT(*) FROM items i JOIN subscriptions s ON i.subscription_id = s.id` + joins
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	var n int64
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, nil, fmt.Errorf("preview count: %w", err)
	}
	items, err := s.ListItemsForView(spec, sample)
	if err != nil {
		return 0, nil, fmt.Errorf("preview: %w", err)
	}
	return n, items, nil
}

// ViewKind is one of the lists the sidebar offers.
type ViewKind string

const (
	ViewAll          ViewKind = "all"
	ViewUnread       ViewKind = "unread"
	ViewStarred      ViewKind = "starred"
	ViewHistory      ViewKind = "history"
	ViewFolder       ViewKind = "folder"
	ViewSubscription ViewKind = "sub"
	ViewTag          ViewKind = "tag"
	ViewPlaylist     ViewKind = "playlist"
)

// Valid reports whether k is one of the kinds above.
func (k ViewKind) Valid() bool {
	switch k {
	case ViewAll, ViewUnread, ViewStarred, ViewHistory, ViewFolder, ViewSubscription, ViewTag, ViewPlaylist:
		return true
	}
	return false
}

// View names a list of items: what the web's view context and a client's
// ListItems both ask for.
type View struct {
	Kind ViewKind
	ID   int64  // the folder, subscription or playlist
	Tag  string // the tag
}

// ResolveView turns a view into what lists it: a ViewSpec, and the
// playlist's query for a playlist (nil otherwise). A playlist that is not
// there is sql.ErrNoRows.
func (s *Store) ResolveView(v View) (ViewSpec, query.Expr, error) {
	switch v.Kind {
	case ViewAll, "":
		return ViewSpec{All: true}, nil, nil
	case ViewUnread:
		return ViewSpec{All: true, HideRead: true}, nil, nil
	case ViewStarred:
		return ViewSpec{Starred: true}, nil, nil
	case ViewHistory:
		return ViewSpec{History: true}, nil, nil
	case ViewFolder:
		return ViewSpec{FolderID: sql.NullInt64{Int64: v.ID, Valid: true}}, nil, nil
	case ViewSubscription:
		return ViewSpec{SubscriptionID: sql.NullInt64{Int64: v.ID, Valid: true}}, nil, nil
	case ViewTag:
		return ViewSpec{TagName: v.Tag}, nil, nil
	case ViewPlaylist:
		pl, err := s.GetPlaylist(v.ID)
		if err != nil {
			return ViewSpec{}, nil, err
		}
		return ViewSpec{All: true, Order: pl.Order, Seed: pl.ShuffleSeed}, playlistQuery(pl), nil
	default:
		return ViewSpec{}, nil, fmt.Errorf("unknown view %q", v.Kind)
	}
}

// and is a AND b, either of which may be nil (every item).
func and(a, b query.Expr) query.Expr {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	default:
		return &query.AndExpr{Left: a, Right: b}
	}
}

// refineBatch is how many rows a page of a query that needs the refine step
// reads from SQL at a time.
const refineBatch = 200

// ViewPage lists up to n items of spec matching expr, after spec.After, and
// returns where the next page starts, or nil at the end. A query that needs
// the refine step reads SQL in batches until the page is full, so a sparse
// search still fills its pages. Relative dates in expr are taken from now.
func (s *Store) ViewPage(spec ViewSpec, expr query.Expr, now time.Time, n int) ([]ItemWithSubscription, *Cursor, error) {
	if expr != nil {
		expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
		spec.Query, spec.Schema = expr, SearchSchema()
	}
	if expr == nil || !queryNeedsRefine(expr) {
		items, keys, err := s.listItems(spec, n+1)
		if err != nil || len(items) <= n {
			return items, nil, err
		}
		return items[:n], &keys[n-1], nil
	}
	var out []ItemWithSubscription
	var outKeys []Cursor
	for {
		batch, keys, err := s.listItems(spec, refineBatch)
		if err != nil {
			return nil, nil, err
		}
		keyOf := make(map[int64]Cursor, len(keys))
		for _, k := range keys {
			keyOf[k.ID] = k
		}
		end := len(batch) < refineBatch
		if len(keys) > 0 {
			spec.After = &keys[len(keys)-1]
		}
		matched, err := s.Refine(batch, expr)
		if err != nil {
			return nil, nil, err
		}
		for _, it := range matched {
			out = append(out, it)
			outKeys = append(outKeys, keyOf[it.ID])
		}
		if len(out) > n {
			return out[:n], &outKeys[n-1], nil
		}
		if end {
			return out, nil, nil
		}
	}
}

// CountView counts spec's items matching expr: in one query when the SQL is
// exact, by refining every candidate when it is not.
func (s *Store) CountView(spec ViewSpec, expr query.Expr, now time.Time) (int64, error) {
	if expr != nil {
		expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
		spec.Query, spec.Schema = expr, SearchSchema()
	}
	if expr != nil && queryNeedsRefine(expr) {
		var n int64
		spec.After = nil
		for {
			items, next, err := s.ViewPage(spec, nil, now, refineBatch)
			if err != nil {
				return 0, err
			}
			matched, err := s.Refine(items, expr)
			if err != nil {
				return 0, err
			}
			n += int64(len(matched))
			if next == nil {
				return n, nil
			}
			spec.After = next
		}
	}
	joins, where, args, err := spec.from()
	if err != nil {
		return 0, fmt.Errorf("count view: %w", err)
	}
	q := `SELECT COUNT(*) FROM items i JOIN subscriptions s ON i.subscription_id = s.id` + joins
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	var n int64
	if err := s.db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count view: %w", err)
	}
	return n, nil
}

// MarkViewRead marks every unread item of spec matching expr read, and
// returns how many it changed: in one statement when the SQL is exact, a
// batch at a time when the query needs the refine step.
func (s *Store) MarkViewRead(spec ViewSpec, expr query.Expr, now time.Time) (int64, error) {
	spec.HideRead, spec.After = true, nil
	if expr != nil {
		expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
	}
	if expr != nil && queryNeedsRefine(expr) {
		var n int64
		// Each batch is read before it is marked, and marking takes it out
		// of the unread items, so the next batch starts after it anyway.
		for {
			items, next, err := s.ViewPage(spec, expr, now, refineBatch)
			if err != nil {
				return 0, err
			}
			ids := make([]int64, len(items))
			for i, it := range items {
				ids[i] = it.ID
			}
			changed, err := s.markRead(ids)
			if err != nil {
				return 0, err
			}
			n += changed
			if next == nil {
				return n, nil
			}
			spec.After = next
		}
	}
	spec.Query, spec.Schema = expr, SearchSchema()
	joins, where, args, err := spec.from()
	if err != nil {
		return 0, fmt.Errorf("mark view read: %w", err)
	}
	q := `UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE id IN (
		SELECT i.id FROM items i JOIN subscriptions s ON i.subscription_id = s.id` + joins +
		" WHERE " + strings.Join(where, " AND ") + ")"
	res, err := s.db.Exec(q, args...)
	if err != nil {
		return 0, fmt.Errorf("mark view read: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark view read rows affected: %w", err)
	}
	return n, nil
}

// markRead marks ids read and returns how many were unread.
func (s *Store) markRead(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	in, args := idArgs(ids)
	res, err := s.db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE is_read = 0 AND id IN (`+in+`)`, args...)
	if err != nil {
		return 0, fmt.Errorf("mark read: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark read rows affected: %w", err)
	}
	return n, nil
}
