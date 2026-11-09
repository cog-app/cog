package feed

import (
	"fmt"
	"log/slog"
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
	for _, x := range PlayOrders {
		if o == x {
			return true
		}
	}
	return false
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

// ListItemsMatching is ListItemsForView with spec.Query set to expr, refined
// to an exact match when the SQL alone is a superset. Relative dates in expr
// are taken from now.
func (s *Store) ListItemsMatching(spec ViewSpec, expr query.Expr, now time.Time, limit int) ([]ItemWithSubscription, error) {
	if expr != nil {
		expr = query.ResolveRelativeDates(expr, *SearchSchema(), now)
		spec.Query, spec.Schema = expr, SearchSchema()
	}
	items, err := s.ListItemsForView(spec, limit)
	if err != nil {
		return nil, err
	}
	if expr != nil && queryNeedsRefine(expr) {
		if items, err = s.Refine(items, expr); err != nil {
			return nil, err
		}
	}
	return items, nil
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
