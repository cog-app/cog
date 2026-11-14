// Package syncprefs holds the user's per-protocol sync settings, for the
// apps that sync with the feed plugin (NextCloud News, GPodder Sync):
//
//   - a filter: a search query that narrows what the protocol exposes, on
//     top of what it can represent at all. It is stored as its query text in
//     feed_settings, with the time it last changed so that a protocol whose
//     clients only ask for changes can tell them about everything the new
//     filter lets through or holds back.
//   - a folder that subscriptions added from the app are put in.
package syncprefs

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// Protocol names a sync protocol.
type Protocol string

const (
	// NCNews is the NextCloud News API, which carries articles: text items,
	// and episodes with show notes.
	NCNews Protocol = "ncnews"
	// GPodder is the NextCloud GPodder Sync API, which carries podcast
	// subscriptions and episode actions.
	GPodder Protocol = "gpodder"
)

// Protocols lists every protocol, in the order settings show them.
var Protocols = []Protocol{NCNews, GPodder}

func (p Protocol) queryKey() string   { return "sync_filter_" + string(p) }
func (p Protocol) changedKey() string { return "sync_filter_" + string(p) + "_changed_at" }

// Filter is a protocol's filter, compiled against the feed schema.
type Filter struct {
	// Query is the user's query text; empty lets everything through.
	Query string
	// ChangedAt is when Query last changed (Unix seconds), 0 if never.
	ChangedAt int64

	where string
	args  []any
	joins string
}

// Cond returns an SQL predicate that holds for the items the filter lets
// through, given the expression naming an item's id (such as "i.id"), and its
// bind arguments, which the caller may append to. It is self-contained, so it can be added to any statement
// on items; it is "1" when there is no query.
func (f Filter) Cond(idColumn string) (string, []any) {
	if f.where == "" {
		return "1", nil
	}
	return idColumn + ` IN (SELECT i.id FROM items i JOIN subscriptions s ON s.id = i.subscription_id` +
		f.joins + ` WHERE ` + f.where + `)`, slices.Clip(f.args)
}

// Compile parses and compiles q against schema, with relative dates taken
// from now. An empty or match-all query compiles to a filter that lets
// everything through.
func Compile(q string, schema query.Schema, now time.Time) (Filter, error) {
	f := Filter{Query: strings.TrimSpace(q)}
	if f.Query == "" {
		return f, nil
	}
	expr, err := query.Parse(f.Query)
	if err != nil {
		return Filter{}, err
	}
	expr = query.ResolveRelativeDates(expr, schema, now)
	where, args, joins, err := query.Compile(expr, schema)
	if err != nil {
		return Filter{}, err
	}
	f.where, f.args = where, args
	for _, j := range joins {
		f.joins += " " + schema.Joins[j]
	}
	return f, nil
}

// Stored returns protocol p's query text and when it last changed (Unix
// seconds, 0 if never), without compiling it.
func Stored(db *sql.DB, p Protocol) (q string, changedAt int64, err error) {
	var changed string
	err = db.QueryRow(`
		SELECT COALESCE(MAX(CASE key WHEN ?1 THEN value END), ''),
		       COALESCE(MAX(CASE key WHEN ?2 THEN value END), '')
		FROM feed_settings WHERE key IN (?1, ?2)`, p.queryKey(), p.changedKey()).Scan(&q, &changed)
	if err != nil {
		return "", 0, fmt.Errorf("syncprefs: load %s: %w", p, err)
	}
	if changed != "" {
		if changedAt, err = strconv.ParseInt(changed, 10, 64); err != nil {
			return "", 0, fmt.Errorf("syncprefs: %s changed_at %q: %w", p, changed, err)
		}
	}
	return q, changedAt, nil
}

// Load reads and compiles protocol p's filter. A stored query that no longer
// compiles is an error rather than no filter: silently exposing everything
// would sync what the user asked to keep out.
func Load(db *sql.DB, p Protocol, schema query.Schema, now time.Time) (Filter, error) {
	q, changedAt, err := Stored(db, p)
	if err != nil {
		return Filter{}, err
	}
	f, err := Compile(q, schema, now)
	if err != nil {
		return Filter{}, fmt.Errorf("syncprefs: compile %s filter %q: %w", p, q, err)
	}
	f.ChangedAt = changedAt
	return f, nil
}

// Save stores f as protocol p's filter, changed at now, in tx.
func Save(tx *sql.Tx, p Protocol, f Filter, now time.Time) error {
	if _, err := tx.Exec(`
		INSERT INTO feed_settings (key, value) VALUES (?1, ?2), (?3, ?4)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		p.queryKey(), f.Query, p.changedKey(), strconv.FormatInt(now.Unix(), 10)); err != nil {
		return fmt.Errorf("syncprefs: save %s: %w", p, err)
	}
	return nil
}

func (p Protocol) folderKey() string { return "sync_folder_" + string(p) }

// Queryer is what reading a setting needs; *sql.DB and *sql.Tx have it.
type Queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

// Folder returns the folder that subscriptions added from protocol p's apps
// go in, or none. A folder deleted since it was chosen is none.
func Folder(q Queryer, p Protocol) (sql.NullInt64, error) {
	var id sql.NullInt64
	err := q.QueryRow(`
		SELECT f.id FROM feed_settings s JOIN folders f ON f.id = CAST(s.value AS INTEGER)
		WHERE s.key = ?`, p.folderKey()).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return sql.NullInt64{}, fmt.Errorf("syncprefs: %s folder: %w", p, err)
	}
	return id, nil
}

// SaveFolder sets the folder that subscriptions added from protocol p's
// apps go in; an invalid id means none.
func SaveFolder(tx *sql.Tx, p Protocol, folderID sql.NullInt64) error {
	value := ""
	if folderID.Valid {
		value = strconv.FormatInt(folderID.Int64, 10)
	}
	if _, err := tx.Exec(`
		INSERT INTO feed_settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, p.folderKey(), value); err != nil {
		return fmt.Errorf("syncprefs: save %s folder: %w", p, err)
	}
	return nil
}
