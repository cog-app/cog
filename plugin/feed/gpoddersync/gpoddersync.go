// Package gpoddersync implements the API of the NextCloud "GPodder Sync" app
// (github.com/thrillfall/nextcloud-gpodder), which AntennaPod and other
// podcast apps use to sync subscriptions and listening progress with a
// NextCloud server.
//
// It works on the feed plugin's schema:
//   - subscriptions → subscriptions WHERE kind='rss' with an episode the
//     user's sync filter lets through, or not fetched yet; removals come from
//     gpodder_removed_subscriptions, which a trigger fills however a
//     subscription is deleted
//   - episode actions → gpodder_episode_actions, one row per episode and
//     action; play actions from Cog's own player are written there by
//     triggers on playback, and uploaded ones are applied to playback
//
// Like ncapi, it depends on a narrow Backend rather than the feed package,
// which imports it.
package gpoddersync

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/plugin/feed/syncprefs"
)

// Prefix is the path the API is served under.
const Prefix = "/index.php/apps/gpoddersync/"

// maxBody bounds a request body. A full sync uploads every subscription at
// once; episode actions arrive in batches of a few dozen.
const maxBody = 8 << 20

// timeLayout is how the app writes episode action timestamps: UTC, without a
// zone.
const timeLayout = "2006-01-02T15:04:05"

// Backend is what the API needs from the feed plugin.
type Backend interface {
	// DB returns the per-user plugin database (migrations already applied).
	DB(username string) (*sql.DB, error)
	// TriggerFetch schedules a fetch of one subscription, or with subID 0 a
	// pass over all of the user's, which also collects orphaned downloads.
	TriggerFetch(username string, subID int64)
	// SyncFilter returns the user's filter for a sync protocol.
	SyncFilter(db *sql.DB, p syncprefs.Protocol) (syncprefs.Filter, error)
}

// API serves the GPodder Sync endpoints.
type API struct {
	backend Backend
	now     func() time.Time
}

// New returns the API backed by b.
func New(b Backend) *API {
	return &API{backend: b, now: time.Now}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		plugin.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	db, err := a.backend.DB(u.Username)
	if err != nil {
		internalError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	switch r.Method + " " + strings.Trim(strings.TrimPrefix(r.URL.Path, Prefix), "/") {
	case "GET subscriptions":
		a.subscriptions(w, r, db)
	case "POST subscription_change/create":
		a.changeSubscriptions(w, r, db, u.Username)
	case "GET episode_action":
		a.episodeActions(w, r, db)
	case "POST episode_action/create":
		a.createEpisodeActions(w, r, db)
	default:
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// since parses the ?since= query parameter, a Unix time in seconds. Absent
// means from the beginning.
func since(w http.ResponseWriter, r *http.Request) (int64, bool) {
	s := r.URL.Query().Get("since")
	if s == "" {
		return 0, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid since " + strconv.Quote(s)})
		return 0, false
	}
	return v, true
}

func internalError(w http.ResponseWriter, r *http.Request, err error) {
	httperr.Log(r, err)
	plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
}

// --- Subscriptions ---

// subscriptions answers what changed since ?since=. Change times are compared
// inclusively: the response's timestamp is the current second, and a change
// made later in that second must not be lost. Repeating one is harmless.
//
// A feed is a podcast to the client if it has an episode (an audio or video
// item) that the user's sync filter lets through. A feed whose items are all
// text is a blog, and is left out; one with no items yet has not been
// fetched, and is included.
//
// When the filter has changed since the client last synced, the answer is
// the whole state rather than what changed: every podcast it lets through,
// and every other feed as removed, since a feed the old filter let through
// may be on the client however long ago it was added.
func (a *API) subscriptions(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	from, ok := since(w, r)
	if !ok {
		return
	}
	now := a.now().Unix()
	f, err := a.backend.SyncFilter(db, syncprefs.GPodder)
	if err != nil {
		internalError(w, r, err)
		return
	}
	refiltered := from > 0 && from <= f.ChangedAt
	addedFrom := from
	if refiltered {
		addedFrom = 0
	}
	cond, args := f.Cond("i.id")
	podcast := `(EXISTS (SELECT 1 FROM items i WHERE i.subscription_id = s.id AND i.type IN ('audio','video') AND ` + cond + `)
		OR NOT EXISTS (SELECT 1 FROM items i WHERE i.subscription_id = s.id))`
	add, err := queryStrings(db, `
		SELECT url FROM subscriptions s
		WHERE kind = 'rss' AND created_at >= ? AND `+podcast+`
		ORDER BY id`, append([]any{addedFrom}, args...)...)
	if err != nil {
		internalError(w, r, fmt.Errorf("gpoddersync: added subscriptions: %w", err))
		return
	}
	removedQuery := `SELECT url FROM gpodder_removed_subscriptions WHERE removed_at >= ?`
	removedArgs := []any{from}
	if refiltered {
		removedQuery += ` UNION SELECT url FROM subscriptions s WHERE kind = 'rss' AND NOT ` + podcast
		removedArgs = append(removedArgs, args...)
	}
	remove, err := queryStrings(db, removedQuery+` ORDER BY url`, removedArgs...)
	if err != nil {
		internalError(w, r, fmt.Errorf("gpoddersync: removed subscriptions: %w", err))
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"add": add, "remove": remove, "timestamp": now})
}

func queryStrings(db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// changeSubscriptions applies a client's subscribes and unsubscribes.
func (a *API) changeSubscriptions(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	var body struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	removing := make(map[string]bool, len(body.Remove))
	for _, u := range body.Remove {
		removing[u] = true
	}
	for _, u := range body.Add {
		if removing[u] {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "both added and removed: " + u})
			return
		}
	}

	added, removed, err := applySubscriptionChanges(db, body.Add, body.Remove)
	if err != nil {
		internalError(w, r, err)
		return
	}
	for _, id := range added {
		a.backend.TriggerFetch(username, id)
	}
	if removed {
		// Downloads of removed feeds count toward the user's quota; have the
		// orchestrator collect them now rather than on its next tick.
		a.backend.TriggerFetch(username, 0)
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"timestamp": a.now().Unix(), "update_urls": [][2]string{}})
}

// applySubscriptionChanges subscribes to add and unsubscribes from remove in
// one transaction, returning the IDs of new subscriptions and whether any were
// deleted. New subscriptions go in the folder the user chose for podcast
// apps. A URL that is not a web address cannot be fetched; it is skipped,
// since refusing the request would fail the client's every sync until it
// unsubscribed.
func applySubscriptionChanges(db *sql.DB, add, remove []string) ([]int64, bool, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, false, fmt.Errorf("gpoddersync: begin: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			slog.Error("gpoddersync: rollback", "err", err)
		}
	}()

	folder, err := syncprefs.Folder(tx, syncprefs.GPodder)
	if err != nil {
		return nil, false, err
	}
	var added []int64
	for _, u := range add {
		if err := validateURL(u); err != nil {
			slog.Info("gpoddersync: skipping subscription", "url", u, "err", err)
			continue
		}
		var id int64
		err := tx.QueryRow(`
			INSERT INTO subscriptions (kind, url, folder_id) VALUES ('rss', ?, ?)
			ON CONFLICT(url) DO NOTHING
			RETURNING id`, u, folder).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("gpoddersync: subscribe %q: %w", u, err)
		}
		added = append(added, id)
	}
	removed := false
	for _, u := range remove {
		res, err := tx.Exec(`DELETE FROM subscriptions WHERE url = ? AND kind = 'rss'`, u)
		if err != nil {
			return nil, false, fmt.Errorf("gpoddersync: unsubscribe %q: %w", u, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return nil, false, fmt.Errorf("gpoddersync: unsubscribe %q rows affected: %w", u, err)
		}
		removed = removed || n > 0
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("gpoddersync: commit subscriptions: %w", err)
	}
	return added, removed, nil
}

func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("empty hostname")
	}
	return nil
}

// --- Episode actions ---

// episodeAction is an action on the wire. Started, position and total are
// seconds and only meaningful for play.
type episodeAction struct {
	Podcast   string `json:"podcast"`
	Episode   string `json:"episode"`
	GUID      string `json:"guid,omitempty"`
	Action    string `json:"action"`
	Timestamp string `json:"timestamp"`
	Started   *int64 `json:"started,omitempty"`
	Position  *int64 `json:"position,omitempty"`
	Total     *int64 `json:"total,omitempty"`
}

// episodeActions answers the actions recorded since ?since=, oldest first so
// a client applying them in order ends on the latest.
func (a *API) episodeActions(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	from, ok := since(w, r)
	if !ok {
		return
	}
	now := a.now().Unix()
	rows, err := db.Query(`
		SELECT podcast, episode, guid, action, timestamp, started, position, total
		FROM gpodder_episode_actions WHERE changed_at >= ?
		ORDER BY timestamp, episode, action`, from)
	if err != nil {
		internalError(w, r, fmt.Errorf("gpoddersync: episode actions: %w", err))
		return
	}
	defer rows.Close()
	actions := []episodeAction{}
	for rows.Next() {
		var ea episodeAction
		var ts, started, position, total int64
		if err := rows.Scan(&ea.Podcast, &ea.Episode, &ea.GUID, &ea.Action, &ts, &started, &position, &total); err != nil {
			internalError(w, r, fmt.Errorf("gpoddersync: scan episode action: %w", err))
			return
		}
		ea.Timestamp = time.Unix(ts, 0).UTC().Format(timeLayout)
		if ea.Action == "play" {
			ea.Started, ea.Position, ea.Total = &started, &position, &total
		}
		actions = append(actions, ea)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, fmt.Errorf("gpoddersync: episode actions: %w", err))
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"actions": actions, "timestamp": now})
}

// createEpisodeActions records a client's actions and applies plays and
// "new" (mark unplayed) to the matching items.
func (a *API) createEpisodeActions(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body []episodeAction
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	now := a.now()
	actions := make([]action, 0, len(body))
	for i, ea := range body {
		act, err := parseAction(ea, now)
		if err != nil {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("action %d: %v", i, err)})
			return
		}
		actions = append(actions, act)
	}
	if err := recordActions(db, actions); err != nil {
		internalError(w, r, err)
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"timestamp": now.Unix(), "update_urls": [][2]string{}})
}

// action is a validated episode action. Absent numbers are -1.
type action struct {
	podcast, episode, guid, kind string
	timestamp                    int64
	started, position, total     int64
}

func parseAction(ea episodeAction, now time.Time) (action, error) {
	act := action{
		podcast: ea.Podcast, episode: ea.Episode, guid: ea.GUID,
		kind:    strings.ToLower(ea.Action),
		started: -1, position: -1, total: -1,
	}
	if act.podcast == "" || act.episode == "" {
		return act, errors.New("podcast and episode are required")
	}
	switch act.kind {
	case "new", "download", "play", "delete":
	default:
		return act, fmt.Errorf("unknown action %q", ea.Action)
	}
	act.timestamp = now.Unix()
	if ea.Timestamp != "" {
		t, err := time.Parse(timeLayout, ea.Timestamp)
		if err != nil {
			if t, err = time.Parse(time.RFC3339, ea.Timestamp); err != nil {
				return act, fmt.Errorf("invalid timestamp %q", ea.Timestamp)
			}
		}
		act.timestamp = t.Unix()
	}
	if act.kind == "play" {
		if ea.Position == nil {
			return act, errors.New("play requires position")
		}
		act.position = *ea.Position
		if ea.Started != nil {
			act.started = *ea.Started
		}
		if ea.Total != nil {
			act.total = *ea.Total
		}
	}
	return act, nil
}

// recordActions stores actions and applies them to playback in one
// transaction. Each is applied only if it is newer than what is there, so a
// phone that was offline cannot rewind progress made since.
//
// A play is applied to playback first: the playback triggers then write a play
// action derived from the item, which the uploaded action replaces so that its
// own fields (started, the client's episode URL) are kept.
func recordActions(db *sql.DB, actions []action) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("gpoddersync: begin: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			slog.Error("gpoddersync: rollback", "err", err)
		}
	}()
	for _, act := range actions {
		if err := applyAction(tx, act); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO gpodder_episode_actions (episode, action, podcast, guid, timestamp, started, position, total, changed_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, unixepoch())
			ON CONFLICT(episode, action) DO UPDATE SET
				podcast = excluded.podcast, guid = excluded.guid, timestamp = excluded.timestamp,
				started = excluded.started, position = excluded.position, total = excluded.total,
				changed_at = excluded.changed_at
			WHERE excluded.timestamp >= gpodder_episode_actions.timestamp`,
			act.episode, act.kind, act.podcast, act.guid, act.timestamp, act.started, act.position, act.total,
		); err != nil {
			return fmt.Errorf("gpoddersync: record %s %q: %w", act.kind, act.episode, err)
		}
		// Played and unplayed exclude each other; an older one of the pair
		// would only contradict the newer for a client syncing from scratch.
		var other string
		switch act.kind {
		case "play":
			other = "new"
		case "new":
			other = "play"
		default:
			continue
		}
		if _, err := tx.Exec(`DELETE FROM gpodder_episode_actions WHERE episode = ? AND action = ? AND timestamp < ?`,
			act.episode, other, act.timestamp); err != nil {
			return fmt.Errorf("gpoddersync: supersede %s %q: %w", other, act.episode, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gpoddersync: commit episode actions: %w", err)
	}
	return nil
}

// applyAction carries a play or new action over to the matching item's
// playback and read state. Download and delete are about the client's own
// storage; they are only recorded.
func applyAction(tx *sql.Tx, act action) error {
	if act.kind != "play" && act.kind != "new" {
		return nil
	}
	// Match on the feed's GUID first: an enclosure URL can change (tracking
	// redirects, CDN moves) while the GUID does not. An episode's item GUID
	// is the entry's with "#" and the enclosure's hash appended (see the
	// gpodder_episodes view); the range finds it through the
	// (subscription_id, guid) index. Of an entry's enclosures, the one the
	// client names wins. Items whose play was missed here pick it up from
	// gpodder_episode_plays when they are fetched.
	var itemID int64
	err := tx.QueryRow(`
		SELECT id FROM (
			SELECT i.id, i.media_url = ?3 AS named FROM items i JOIN subscriptions s ON s.id = i.subscription_id
			WHERE ?1 != '' AND s.url = ?2 AND i.type IN ('audio', 'video')
			  AND (i.guid = ?1 OR (i.guid > ?1 || '#' AND i.guid < ?1 || '$'))
			ORDER BY named DESC LIMIT 1)
		UNION ALL
		SELECT id FROM items WHERE media_url = ?3 AND type IN ('audio', 'video')
		LIMIT 1`, act.guid, act.podcast, act.episode).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("gpoddersync: find item %q: %w", act.episode, err)
	}

	if act.kind == "new" {
		res, err := tx.Exec(`DELETE FROM playback WHERE item_id = ? AND last_played_at <= ?`, itemID, act.timestamp)
		if err != nil {
			return fmt.Errorf("gpoddersync: reset playback %d: %w", itemID, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("gpoddersync: reset playback %d rows affected: %w", itemID, err)
		}
		var exists bool
		if n == 0 {
			if err := tx.QueryRow(`SELECT EXISTS (SELECT 1 FROM playback WHERE item_id = ?)`, itemID).Scan(&exists); err != nil {
				return fmt.Errorf("gpoddersync: playback %d: %w", itemID, err)
			}
		}
		if exists {
			// Played here after the client marked it unplayed.
			return nil
		}
		if _, err := tx.Exec(`UPDATE items SET is_read = 0, fetched_at = unixepoch() WHERE id = ?`, itemID); err != nil {
			return fmt.Errorf("gpoddersync: mark %d unread: %w", itemID, err)
		}
		return nil
	}

	// AntennaPod reports a finished episode as position == total. Cog keeps a
	// finished episode's progress at 0, as its player does.
	completed := act.total > 0 && act.position >= act.total
	progress := act.position
	if completed {
		progress = 0
	}
	res, err := tx.Exec(`
		INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			progress_sec = excluded.progress_sec,
			completed = excluded.completed,
			last_played_at = excluded.last_played_at
		WHERE excluded.last_played_at >= playback.last_played_at`,
		itemID, progress, completed, act.timestamp)
	if err != nil {
		return fmt.Errorf("gpoddersync: set playback %d: %w", itemID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("gpoddersync: set playback %d rows affected: %w", itemID, err)
	}
	if n > 0 && completed {
		if _, err := tx.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE id = ?`, itemID); err != nil {
			return fmt.Errorf("gpoddersync: mark %d read: %w", itemID, err)
		}
	}
	return nil
}
