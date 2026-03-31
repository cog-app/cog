package media

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// SourceKind identifies the type of media source.
type SourceKind string

const (
	SourcePodcast SourceKind = "podcast"
	SourceYouTube SourceKind = "youtube"
	SourceURL     SourceKind = "url"
)

// MediaType distinguishes audio from video content.
type MediaType string

const (
	MediaAudio MediaType = "audio"
	MediaVideo MediaType = "video"
)

// Group is a logical grouping of sources.
type Group struct {
	ID        int64
	Name      string
	SortOrder int
	CreatedAt int64
}

// Source is a subscription to a podcast feed, YouTube channel, or arbitrary URL.
type Source struct {
	ID              int64
	GroupID         sql.NullInt64
	Kind            SourceKind
	URL             string
	Title           string
	Description     string
	ImageURL        string
	ETag            string
	LastModified    string
	PollIntervalSec int
	NextCheckAt     int64
	LastCheckedAt   int64
	LastSuccessAt   int64
	ErrorCount      int
	LastError       string
	Paused          bool
	CreatedAt       int64
}

// Episode is an individual media item from a source.
type Episode struct {
	ID           int64
	SourceID     int64
	GUID         string
	Title        string
	Description  string
	MediaURL     string
	MediaType    MediaType
	DurationSec  int
	ThumbnailURL string
	PublishedAt  int64
	FetchedAt    int64
}

// EpisodeView extends Episode with display-time data.
type EpisodeView struct {
	Episode
	SourceTitle string
	ProgressSec float64
	Completed   bool
	InQueue     bool
	QueuePos    int
	Tags        []string
}

// Playback tracks resume position for an episode.
type Playback struct {
	EpisodeID    int64
	ProgressSec  float64
	Completed    bool
	LastPlayedAt int64
}

// --- Group CRUD ---

func ListGroups(db *sql.DB) ([]Group, error) {
	rows, err := db.Query(`SELECT id, name, sort_order, created_at FROM groups ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.SortOrder, &g.CreatedAt); err != nil {
			return nil, err
		}
		groups = append(groups, g)
	}
	return groups, rows.Err()
}

func CreateGroup(db *sql.DB, name string) (*Group, error) {
	res, err := db.Exec(`INSERT INTO groups (name) VALUES (?)`, name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Group{ID: id, Name: name}, nil
}

func RenameGroup(db *sql.DB, id int64, name string) error {
	_, err := db.Exec(`UPDATE groups SET name = ? WHERE id = ?`, name, id)
	return err
}

func DeleteGroup(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM groups WHERE id = ?`, id)
	return err
}

// --- Source CRUD ---

var sourceCols = `id, group_id, kind, url, title, description, image_url,
	etag, last_modified, poll_interval_sec, next_check_at, last_checked_at,
	last_success_at, error_count, last_error, paused, created_at`

func scanSource(row interface{ Scan(...any) error }) (*Source, error) {
	var s Source
	err := row.Scan(
		&s.ID, &s.GroupID, &s.Kind, &s.URL, &s.Title, &s.Description, &s.ImageURL,
		&s.ETag, &s.LastModified, &s.PollIntervalSec, &s.NextCheckAt, &s.LastCheckedAt,
		&s.LastSuccessAt, &s.ErrorCount, &s.LastError, &s.Paused, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func ListSources(db *sql.DB) ([]Source, error) {
	rows, err := db.Query(`SELECT ` + sourceCols + ` FROM sources ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSources(rows)
}

func GetSource(db *sql.DB, id int64) (*Source, error) {
	return scanSource(db.QueryRow(`SELECT `+sourceCols+` FROM sources WHERE id = ?`, id))
}

func CreateSource(db *sql.DB, kind SourceKind, url string, groupID sql.NullInt64) (*Source, error) {
	res, err := db.Exec(`INSERT INTO sources (kind, url, group_id) VALUES (?, ?, ?)`, kind, url, groupID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetSource(db, id)
}

func UpdateSourceMeta(db *sql.DB, id int64, title, description, imageURL string) error {
	_, err := db.Exec(`UPDATE sources SET title = ?, description = ?, image_url = ? WHERE id = ?`,
		title, description, imageURL, id)
	return err
}

func DeleteSource(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM sources WHERE id = ?`, id)
	return err
}

func SourcesDue(db *sql.DB, now int64) ([]Source, error) {
	rows, err := db.Query(`SELECT `+sourceCols+` FROM sources WHERE next_check_at <= ? AND paused = 0`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSources(rows)
}

func RecordSourceError(db *sql.DB, s *Source, fetchErr error, now int64) error {
	newCount := s.ErrorCount + 1
	// Exponential backoff: min(1800 * 2^count, 86400)
	backoff := int64(1800)
	for i := 0; i < newCount && backoff < 86400; i++ {
		backoff *= 2
	}
	if backoff > 86400 {
		backoff = 86400
	}
	_, err := db.Exec(`UPDATE sources SET last_checked_at = ?, error_count = ?, last_error = ?,
		next_check_at = ? WHERE id = ?`,
		now, newCount, fetchErr.Error(), now+backoff, s.ID)
	if err != nil {
		return err
	}
	return fetchErr
}

func RecordSourceSuccess(db *sql.DB, s *Source, etag, lastMod string, now int64) error {
	_, err := db.Exec(`UPDATE sources SET etag = ?, last_modified = ?,
		last_checked_at = ?, last_success_at = ?, next_check_at = ?,
		error_count = 0, last_error = '' WHERE id = ?`,
		etag, lastMod, now, now, now+int64(s.PollIntervalSec), s.ID)
	return err
}

func scanSources(rows *sql.Rows) ([]Source, error) {
	var sources []Source
	for rows.Next() {
		var s Source
		if err := rows.Scan(
			&s.ID, &s.GroupID, &s.Kind, &s.URL, &s.Title, &s.Description, &s.ImageURL,
			&s.ETag, &s.LastModified, &s.PollIntervalSec, &s.NextCheckAt, &s.LastCheckedAt,
			&s.LastSuccessAt, &s.ErrorCount, &s.LastError, &s.Paused, &s.CreatedAt); err != nil {
			return nil, err
		}
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

// --- Episode CRUD ---

func UpsertEpisode(db *sql.DB, e *Episode) (int64, error) {
	res, err := db.Exec(`INSERT INTO episodes (source_id, guid, title, description, media_url, media_type, duration_sec, thumbnail_url, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source_id, guid) DO UPDATE SET
			title = excluded.title,
			description = excluded.description,
			media_url = excluded.media_url,
			media_type = excluded.media_type,
			duration_sec = excluded.duration_sec,
			thumbnail_url = excluded.thumbnail_url,
			published_at = excluded.published_at`,
		e.SourceID, e.GUID, e.Title, e.Description, e.MediaURL, e.MediaType, e.DurationSec, e.ThumbnailURL, e.PublishedAt)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func GetEpisode(db *sql.DB, id int64) (*EpisodeView, error) {
	var ev EpisodeView
	var progressSec sql.NullFloat64
	var completed sql.NullBool
	var queuePos sql.NullInt64
	err := db.QueryRow(`SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM episodes e
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN playback p ON p.episode_id = e.id
		LEFT JOIN queue q ON q.episode_id = e.id
		WHERE e.id = ?`, id).Scan(
		&ev.ID, &ev.SourceID, &ev.GUID, &ev.Title, &ev.Description,
		&ev.MediaURL, &ev.MediaType, &ev.DurationSec, &ev.ThumbnailURL, &ev.PublishedAt, &ev.FetchedAt,
		&ev.SourceTitle,
		&progressSec, &completed,
		&queuePos)
	if err != nil {
		return nil, err
	}
	if progressSec.Valid {
		ev.ProgressSec = progressSec.Float64
	}
	if completed.Valid {
		ev.Completed = completed.Bool
	}
	ev.InQueue = queuePos.Valid
	if queuePos.Valid {
		ev.QueuePos = int(queuePos.Int64)
	}
	ev.Tags, _ = EpisodeTags(db, id)
	return &ev, nil
}

func ListEpisodesBySource(db *sql.DB, sourceID int64, limit int) ([]EpisodeView, error) {
	return queryEpisodes(db, `SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM episodes e
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN playback p ON p.episode_id = e.id
		LEFT JOIN queue q ON q.episode_id = e.id
		WHERE e.source_id = ?
		ORDER BY e.published_at DESC LIMIT ?`, sourceID, limit)
}

func ListRecentEpisodes(db *sql.DB, limit int) ([]EpisodeView, error) {
	return queryEpisodes(db, `SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM episodes e
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN playback p ON p.episode_id = e.id
		LEFT JOIN queue q ON q.episode_id = e.id
		ORDER BY e.published_at DESC LIMIT ?`, limit)
}

func ListEpisodesByTag(db *sql.DB, tag string, limit int) ([]EpisodeView, error) {
	return queryEpisodes(db, `SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM episodes e
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN playback p ON p.episode_id = e.id
		LEFT JOIN queue q ON q.episode_id = e.id
		JOIN episode_tags et ON et.episode_id = e.id
		JOIN tags t ON t.id = et.tag_id
		WHERE t.name = ?
		ORDER BY e.published_at DESC LIMIT ?`, tag, limit)
}

func queryEpisodes(db *sql.DB, query string, args ...any) ([]EpisodeView, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var episodes []EpisodeView
	for rows.Next() {
		var ev EpisodeView
		var progressSec sql.NullFloat64
		var completed sql.NullBool
		var queuePos sql.NullInt64
		if err := rows.Scan(
			&ev.ID, &ev.SourceID, &ev.GUID, &ev.Title, &ev.Description,
			&ev.MediaURL, &ev.MediaType, &ev.DurationSec, &ev.ThumbnailURL, &ev.PublishedAt, &ev.FetchedAt,
			&ev.SourceTitle,
			&progressSec, &completed,
			&queuePos); err != nil {
			return nil, err
		}
		if progressSec.Valid {
			ev.ProgressSec = progressSec.Float64
		}
		if completed.Valid {
			ev.Completed = completed.Bool
		}
		ev.InQueue = queuePos.Valid
		if queuePos.Valid {
			ev.QueuePos = int(queuePos.Int64)
		}
		episodes = append(episodes, ev)
	}
	return episodes, rows.Err()
}

// --- Queue ---

func ListQueue(db *sql.DB) ([]EpisodeView, error) {
	return queryEpisodes(db, `SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM queue q
		JOIN episodes e ON e.id = q.episode_id
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN playback p ON p.episode_id = e.id
		ORDER BY q.position`)
}

func AddToQueue(db *sql.DB, episodeID int64) error {
	_, err := db.Exec(`INSERT OR IGNORE INTO queue (episode_id, position)
		VALUES (?, COALESCE((SELECT MAX(position) FROM queue), 0) + 1)`, episodeID)
	return err
}

// QueueIfNew adds an episode to the queue unless it was already completed.
func QueueIfNew(db *sql.DB, episodeID int64) error {
	var completed int
	err := db.QueryRow(`SELECT completed FROM playback WHERE episode_id = ?`, episodeID).Scan(&completed)
	if err == nil && completed == 1 {
		return nil // already completed, don't re-queue
	}
	return AddToQueue(db, episodeID)
}

func RemoveFromQueue(db *sql.DB, episodeID int64) error {
	_, err := db.Exec(`DELETE FROM queue WHERE episode_id = ?`, episodeID)
	return err
}

func MoveInQueue(db *sql.DB, episodeID int64, newPosition int) error {
	// Get current position.
	var oldPos int
	err := db.QueryRow(`SELECT position FROM queue WHERE episode_id = ?`, episodeID).Scan(&oldPos)
	if err != nil {
		return err
	}
	if oldPos == newPosition {
		return nil
	}
	if newPosition > oldPos {
		// Moving down: shift items between old+1..new up by 1.
		db.Exec(`UPDATE queue SET position = position - 1 WHERE position > ? AND position <= ?`, oldPos, newPosition)
	} else {
		// Moving up: shift items between new..old-1 down by 1.
		db.Exec(`UPDATE queue SET position = position + 1 WHERE position >= ? AND position < ?`, newPosition, oldPos)
	}
	_, err = db.Exec(`UPDATE queue SET position = ? WHERE episode_id = ?`, newPosition, episodeID)
	return err
}

func QueueCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&n)
	return n, err
}

// --- Playback ---

func SaveProgress(db *sql.DB, episodeID int64, progressSec float64, now int64) error {
	_, err := db.Exec(`INSERT INTO playback (episode_id, progress_sec, last_played_at)
		VALUES (?, ?, ?)
		ON CONFLICT(episode_id) DO UPDATE SET progress_sec = excluded.progress_sec, last_played_at = excluded.last_played_at`,
		episodeID, progressSec, now)
	return err
}

func MarkCompleted(db *sql.DB, episodeID int64, now int64) error {
	_, err := db.Exec(`INSERT INTO playback (episode_id, progress_sec, completed, last_played_at)
		VALUES (?, 0, 1, ?)
		ON CONFLICT(episode_id) DO UPDATE SET completed = 1, last_played_at = excluded.last_played_at`,
		episodeID, now)
	if err != nil {
		return err
	}
	return RemoveFromQueue(db, episodeID)
}

func RecentlyPlayed(db *sql.DB, limit int) ([]EpisodeView, error) {
	return queryEpisodes(db, `SELECT e.id, e.source_id, e.guid, e.title, e.description,
		e.media_url, e.media_type, e.duration_sec, e.thumbnail_url, e.published_at, e.fetched_at,
		s.title,
		p.progress_sec, p.completed,
		q.position
		FROM playback p
		JOIN episodes e ON e.id = p.episode_id
		JOIN sources s ON e.source_id = s.id
		LEFT JOIN queue q ON q.episode_id = e.id
		WHERE p.last_played_at > 0
		ORDER BY p.last_played_at DESC LIMIT ?`, limit)
}

// --- Tags ---

func EpisodeTags(db *sql.DB, episodeID int64) ([]string, error) {
	rows, err := db.Query(`SELECT t.name FROM tags t
		JOIN episode_tags et ON et.tag_id = t.id
		WHERE et.episode_id = ?
		ORDER BY t.name`, episodeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}

func ListTags(db *sql.DB) ([]string, error) {
	rows, err := db.Query(`SELECT name FROM tags ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}

func TagEpisode(db *sql.DB, episodeID int64, tagName string) error {
	tagName = strings.TrimSpace(tagName)
	if tagName == "" {
		return fmt.Errorf("empty tag name")
	}
	_, err := db.Exec(`INSERT OR IGNORE INTO tags (name) VALUES (?)`, tagName)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO episode_tags (episode_id, tag_id)
		SELECT ?, id FROM tags WHERE name = ?`, episodeID, tagName)
	return err
}

func UntagEpisode(db *sql.DB, episodeID int64, tagName string) error {
	_, err := db.Exec(`DELETE FROM episode_tags WHERE episode_id = ? AND tag_id = (SELECT id FROM tags WHERE name = ?)`,
		episodeID, tagName)
	return err
}

func AutoTagEpisode(db *sql.DB, episodeID int64, mediaType MediaType) error {
	return TagEpisode(db, episodeID, string(mediaType))
}

// SourceEpisodeCount returns the number of episodes for a source.
func SourceEpisodeCount(db *sql.DB, sourceID int64) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE source_id = ?`, sourceID).Scan(&n)
	return n, err
}

// SourceEpisodeCounts returns episode counts keyed by source ID.
func SourceEpisodeCounts(db *sql.DB) (map[int64]int, error) {
	rows, err := db.Query(`SELECT source_id, COUNT(*) FROM episodes GROUP BY source_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[int64]int)
	for rows.Next() {
		var sid int64
		var n int
		if err := rows.Scan(&sid, &n); err != nil {
			return nil, err
		}
		counts[sid] = n
	}
	return counts, rows.Err()
}

// --- Filters ---

// Filter is a user-level rule that matches episodes and applies an action.
// Use :source=name in the query to scope a filter to a specific source.
type Filter struct {
	ID          int64
	SortOrder   int
	Query       string
	Action      string // "skip", "tag", "mute_profanity"
	ActionValue string
	CreatedAt   int64
}

func ListFilters(db *sql.DB) ([]Filter, error) {
	rows, err := db.Query(`SELECT id, sort_order, query, action, action_value, created_at
		FROM filters ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var filters []Filter
	for rows.Next() {
		var f Filter
		if err := rows.Scan(&f.ID, &f.SortOrder, &f.Query, &f.Action, &f.ActionValue, &f.CreatedAt); err != nil {
			return nil, err
		}
		filters = append(filters, f)
	}
	return filters, rows.Err()
}

func CreateFilter(db *sql.DB, queryStr, action, actionValue string) (*Filter, error) {
	// Validate query parses (or is wildcard).
	if queryStr != "*" {
		if _, err := query.Parse(queryStr); err != nil {
			return nil, fmt.Errorf("invalid filter query: %w", err)
		}
	}
	var maxOrder int
	db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) FROM filters`).Scan(&maxOrder)
	res, err := db.Exec(`INSERT INTO filters (sort_order, query, action, action_value) VALUES (?, ?, ?, ?)`,
		maxOrder+1, queryStr, action, actionValue)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Filter{ID: id, SortOrder: maxOrder + 1, Query: queryStr, Action: action, ActionValue: actionValue}, nil
}

func DeleteFilter(db *sql.DB, filterID int64) error {
	_, err := db.Exec(`DELETE FROM filters WHERE id = ?`, filterID)
	return err
}

// ApplyFilters evaluates all user-level filters against an episode.
// The source title is included as the "source" field for filtering.
// Returns true if the episode should be skipped from the queue.
func ApplyFilters(db *sql.DB, sourceTitle string, episodeID int64, ep *Episode) bool {
	filters, err := ListFilters(db)
	if err != nil || len(filters) == 0 {
		return false
	}

	rec := query.Record{
		"title":       ep.Title,
		"description": ep.Description,
		"type":        string(ep.MediaType),
		"duration":    strconv.Itoa(ep.DurationSec),
		"url":         ep.MediaURL,
		"source":      sourceTitle,
	}

	skip := false
	for _, f := range filters {
		if !filterMatches(f.Query, rec) {
			continue
		}
		switch f.Action {
		case "skip":
			skip = true
		case "tag":
			if f.ActionValue != "" {
				TagEpisode(db, episodeID, f.ActionValue)
			}
		case "mute_profanity":
			TagEpisode(db, episodeID, "_mute_profanity")
		}
	}
	return skip
}

func filterMatches(q string, rec query.Record) bool {
	if q == "*" {
		return true
	}
	expr, err := query.Parse(q)
	if err != nil {
		return false
	}
	return expr.Match(rec)
}

// FormatDuration formats seconds as "1h 23m" or "23m" or "1m".
func FormatDuration(sec int) string {
	if sec <= 0 {
		return ""
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m == 0 {
		m = 1
	}
	return fmt.Sprintf("%dm", m)
}
