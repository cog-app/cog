package feed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/query"
)

// Store provides access to the feed plugin's per-user database.
type Store struct {
	db *sql.DB
}

// NewStore wraps a *sql.DB in a Store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Folder is a logical grouping of subscriptions.
type Folder struct {
	ID        int64
	Name      string
	CreatedAt int64
}

// Subscription is a feed subscription source.
type Subscription struct {
	ID              int64
	FolderID        sql.NullInt64
	Kind            string
	URL             string
	Title           string
	Description     string
	SiteURL         string
	ImageURL        string
	FaviconURL      string
	ETag            string
	LastModified    string
	PollIntervalSec int64
	NextCheckAt     int64
	LastCheckedAt   int64
	LastSuccessAt   int64
	ErrorCount      int64
	LastError       string
	Paused          bool
	CreatedAt       int64
}

// --- Folder methods ---

// CreateFolder inserts a new folder with the given name and returns its ID.
func (s *Store) CreateFolder(name string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO folders (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("create folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create folder last insert id: %w", err)
	}
	return id, nil
}

// ListFolders returns all folders ordered by name.
func (s *Store) ListFolders() ([]Folder, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at FROM folders ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	var folders []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("list folders scan: %w", err)
		}
		folders = append(folders, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list folders rows: %w", err)
	}
	return folders, nil
}

// RenameFolder updates a folder's name.
func (s *Store) RenameFolder(id int64, name string) error {
	_, err := s.db.Exec(`UPDATE folders SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("rename folder: %w", err)
	}
	return nil
}

// GetFolder returns a single folder by ID.
func (s *Store) GetFolder(id int64) (Folder, error) {
	var f Folder
	row := s.db.QueryRow(`SELECT id, name, created_at FROM folders WHERE id = ?`, id)
	if err := row.Scan(&f.ID, &f.Name, &f.CreatedAt); err != nil {
		return f, fmt.Errorf("get folder: %w", err)
	}
	return f, nil
}

// DeleteFolder removes a folder by ID. Subscriptions in the folder have their
// folder_id set to NULL (enforced by the ON DELETE SET NULL constraint).
func (s *Store) DeleteFolder(id int64) error {
	_, err := s.db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	return nil
}

// DeleteFolders deletes all folders with the given ids in a single statement.
// Subscriptions in those folders have their folder_id set to NULL via the
// ON DELETE SET NULL foreign-key constraint. Empty input is a no-op.
func (s *Store) DeleteFolders(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(
		`DELETE FROM folders WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("bulk delete folders: %w", err)
	}
	return nil
}

// --- Subscription methods ---

// CreateSubscription inserts a new subscription and returns its ID.
func (s *Store) CreateSubscription(sub Subscription) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO subscriptions
			(folder_id, kind, url, title, description, site_url, image_url, favicon_url,
			 etag, last_modified, poll_interval_sec, next_check_at, last_checked_at,
			 last_success_at, error_count, last_error, paused)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sub.FolderID, sub.Kind, sub.URL, sub.Title, sub.Description,
		sub.SiteURL, sub.ImageURL, sub.FaviconURL,
		sub.ETag, sub.LastModified, sub.PollIntervalSec, sub.NextCheckAt,
		sub.LastCheckedAt, sub.LastSuccessAt, sub.ErrorCount, sub.LastError,
		boolToInt(sub.Paused),
	)
	if err != nil {
		return 0, fmt.Errorf("create subscription: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create subscription last insert id: %w", err)
	}
	return id, nil
}

// GetSubscription returns a single subscription by ID.
func (s *Store) GetSubscription(id int64) (Subscription, error) {
	var sub Subscription
	var paused int
	err := s.db.QueryRow(`
		SELECT id, folder_id, kind, url, title, description, site_url, image_url,
		       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
		       last_checked_at, last_success_at, error_count, last_error, paused, created_at
		FROM subscriptions WHERE id = ?`, id).Scan(
		&sub.ID, &sub.FolderID, &sub.Kind, &sub.URL, &sub.Title, &sub.Description,
		&sub.SiteURL, &sub.ImageURL, &sub.FaviconURL, &sub.ETag, &sub.LastModified,
		&sub.PollIntervalSec, &sub.NextCheckAt, &sub.LastCheckedAt, &sub.LastSuccessAt,
		&sub.ErrorCount, &sub.LastError, &paused, &sub.CreatedAt,
	)
	if err != nil {
		return Subscription{}, fmt.Errorf("get subscription: %w", err)
	}
	sub.Paused = paused != 0
	return sub, nil
}

// ListSubscriptions returns all subscriptions.
func (s *Store) ListSubscriptions() ([]Subscription, error) {
	rows, err := s.db.Query(`
		SELECT id, folder_id, kind, url, title, description, site_url, image_url,
		       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
		       last_checked_at, last_success_at, error_count, last_error, paused, created_at
		FROM subscriptions ORDER BY (CASE WHEN error_count > 0 THEN 0 ELSE 1 END) ASC, LOWER(COALESCE(NULLIF(title, ''), url)) ASC`)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	return scanSubscriptions(rows)
}

// ListSubscriptionsByFolder returns subscriptions in a given folder (or unfoldered if null).
func (s *Store) ListSubscriptionsByFolder(folderID sql.NullInt64) ([]Subscription, error) {
	var rows *sql.Rows
	var err error
	if folderID.Valid {
		rows, err = s.db.Query(`
			SELECT id, folder_id, kind, url, title, description, site_url, image_url,
			       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
			       last_checked_at, last_success_at, error_count, last_error, paused, created_at
			FROM subscriptions WHERE folder_id = ? ORDER BY title`, folderID.Int64)
	} else {
		rows, err = s.db.Query(`
			SELECT id, folder_id, kind, url, title, description, site_url, image_url,
			       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
			       last_checked_at, last_success_at, error_count, last_error, paused, created_at
			FROM subscriptions WHERE folder_id IS NULL ORDER BY title`)
	}
	if err != nil {
		return nil, fmt.Errorf("list subscriptions by folder: %w", err)
	}
	return scanSubscriptions(rows)
}

// UpdateSubscriptionPollState updates polling metadata after a successful fetch.
func (s *Store) UpdateSubscriptionPollState(id int64, etag, lastMod string, nextCheckAt, lastCheckedAt, lastSuccessAt int64) error {
	_, err := s.db.Exec(`
		UPDATE subscriptions SET
			etag = ?, last_modified = ?,
			next_check_at = ?, last_checked_at = ?, last_success_at = ?,
			error_count = 0, last_error = ''
		WHERE id = ?`,
		etag, lastMod, nextCheckAt, lastCheckedAt, lastSuccessAt, id,
	)
	if err != nil {
		return fmt.Errorf("update subscription poll state: %w", err)
	}
	return nil
}

// allowedMetadataKeys is the set of subscription columns that may be updated via UpdateSubscriptionMetadata.
var allowedMetadataKeys = map[string]bool{
	"title":       true,
	"description": true,
	"site_url":    true,
	"image_url":   true,
	"favicon_url": true,
}

// UpdateSubscriptionMetadata updates a small set of allowed string columns.
// Unknown keys are silently skipped; keys are never interpolated directly into SQL.
func (s *Store) UpdateSubscriptionMetadata(id int64, updates map[string]string) error {
	var setClauses []string
	var args []any
	for _, key := range []string{"title", "description", "site_url", "image_url", "favicon_url"} {
		val, ok := updates[key]
		if !ok {
			continue
		}
		if !allowedMetadataKeys[key] {
			continue
		}
		setClauses = append(setClauses, key+" = ?")
		args = append(args, val)
	}
	if len(setClauses) == 0 {
		return nil
	}
	args = append(args, id)
	query := "UPDATE subscriptions SET " + strings.Join(setClauses, ", ") + " WHERE id = ?"
	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("update subscription metadata: %w", err)
	}
	return nil
}

// RecordSubscriptionError increments the error count and schedules a retry.
func (s *Store) RecordSubscriptionError(id int64, errMsg string, nextCheckAt int64) error {
	_, err := s.db.Exec(`
		UPDATE subscriptions SET
			error_count = error_count + 1, last_error = ?,
			next_check_at = ?, last_checked_at = unixepoch()
		WHERE id = ?`,
		errMsg, nextCheckAt, id,
	)
	if err != nil {
		return fmt.Errorf("record subscription error: %w", err)
	}
	return nil
}

// UpdateSubscriptionNextCheck sets next_check_at on a subscription.
// Set to 1 to trigger an immediate re-poll on the next orchestrator tick.
func (s *Store) UpdateSubscriptionNextCheck(id int64, nextCheckAt int64) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET next_check_at = ? WHERE id = ?`, nextCheckAt, id)
	if err != nil {
		return fmt.Errorf("update subscription next check: %w", err)
	}
	return nil
}

// PauseSubscription sets or clears the paused flag on a subscription.
func (s *Store) PauseSubscription(id int64, paused bool) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET paused = ? WHERE id = ?`, boolToInt(paused), id)
	if err != nil {
		return fmt.Errorf("pause subscription: %w", err)
	}
	return nil
}

// DeleteSubscription removes a subscription by ID.
func (s *Store) DeleteSubscription(id int64) error {
	_, err := s.db.Exec(`DELETE FROM subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

// DeleteSubscriptions deletes all subscriptions with the given ids in a single
// statement. Child rows (items, playback, downloads, item_tags) are removed via
// ON DELETE CASCADE foreign-key constraints. Empty input is a no-op.
func (s *Store) DeleteSubscriptions(ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(
		`DELETE FROM subscriptions WHERE id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("bulk delete subscriptions: %w", err)
	}
	return nil
}

// ListDueSubscriptions returns non-paused subscriptions whose next_check_at is <= now.
func (s *Store) ListDueSubscriptions(now int64) ([]Subscription, error) {
	rows, err := s.db.Query(`
		SELECT id, folder_id, kind, url, title, description, site_url, image_url,
		       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
		       last_checked_at, last_success_at, error_count, last_error, paused, created_at
		FROM subscriptions WHERE next_check_at <= ? AND paused = 0 ORDER BY next_check_at`, now)
	if err != nil {
		return nil, fmt.Errorf("list due subscriptions: %w", err)
	}
	return scanSubscriptions(rows)
}

// scanSubscriptions reads subscription rows into a slice.
func scanSubscriptions(rows *sql.Rows) ([]Subscription, error) {
	defer rows.Close()
	var subs []Subscription
	for rows.Next() {
		var sub Subscription
		var paused int
		if err := rows.Scan(
			&sub.ID, &sub.FolderID, &sub.Kind, &sub.URL, &sub.Title, &sub.Description,
			&sub.SiteURL, &sub.ImageURL, &sub.FaviconURL, &sub.ETag, &sub.LastModified,
			&sub.PollIntervalSec, &sub.NextCheckAt, &sub.LastCheckedAt, &sub.LastSuccessAt,
			&sub.ErrorCount, &sub.LastError, &paused, &sub.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		sub.Paused = paused != 0
		subs = append(subs, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan subscriptions rows: %w", err)
	}
	return subs, nil
}

// boolToInt converts a bool to 0 or 1 for SQLite storage.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// --- Item type ---

// Item represents a single entry fetched from a subscription.
type Item struct {
	ID             int64
	SubscriptionID int64
	GUID           string
	Type           string // text | audio | video
	Title          string
	Author         string
	URL            string
	ContentHTML    string
	MediaURL       string
	MediaMIME      string
	DurationSec    int64
	ThumbnailURL   string
	PublishedAt    int64
	FetchedAt      int64
	IsRead         bool
	IsStarred      bool
	ContentHash    string
}

// --- Item methods ---

// UpsertItem inserts an item; on (subscription_id, guid) conflict returns the
// existing id with inserted=false. On success returns the new id with inserted=true.
func (s *Store) UpsertItem(it Item) (int64, bool, error) {
	res, err := s.db.Exec(`
		INSERT OR IGNORE INTO items
			(subscription_id, guid, type, title, author, url, content_html,
			 media_url, media_mime, duration_sec, thumbnail_url, published_at,
			 is_read, is_starred, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.SubscriptionID, it.GUID, it.Type, it.Title, it.Author, it.URL, it.ContentHTML,
		it.MediaURL, it.MediaMIME, it.DurationSec, it.ThumbnailURL, it.PublishedAt,
		boolToInt(it.IsRead), boolToInt(it.IsStarred), it.ContentHash,
	)
	if err != nil {
		return 0, false, fmt.Errorf("upsert item: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("upsert item rows affected: %w", err)
	}
	if affected > 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("upsert item last insert id: %w", err)
		}
		return id, true, nil
	}
	// Conflict: return existing id.
	var id int64
	err = s.db.QueryRow(`SELECT id FROM items WHERE subscription_id = ? AND guid = ?`,
		it.SubscriptionID, it.GUID).Scan(&id)
	if err != nil {
		return 0, false, fmt.Errorf("upsert item select existing: %w", err)
	}
	return id, false, nil
}

// GetItem returns a single item by ID.
func (s *Store) GetItem(id int64) (Item, error) {
	var it Item
	var isRead, isStarred int
	err := s.db.QueryRow(`
		SELECT id, subscription_id, guid, type, title, author, url, content_html,
		       media_url, media_mime, duration_sec, thumbnail_url, published_at,
		       fetched_at, is_read, is_starred, content_hash
		FROM items WHERE id = ?`, id).Scan(
		&it.ID, &it.SubscriptionID, &it.GUID, &it.Type, &it.Title, &it.Author,
		&it.URL, &it.ContentHTML, &it.MediaURL, &it.MediaMIME, &it.DurationSec,
		&it.ThumbnailURL, &it.PublishedAt, &it.FetchedAt, &isRead, &isStarred,
		&it.ContentHash,
	)
	if err != nil {
		return Item{}, fmt.Errorf("get item: %w", err)
	}
	it.IsRead = isRead != 0
	it.IsStarred = isStarred != 0
	return it, nil
}

// DeleteItem removes an item by ID.
func (s *Store) DeleteItem(id int64) error {
	_, err := s.db.Exec(`DELETE FROM items WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete item: %w", err)
	}
	return nil
}

// MarkRead sets or clears the is_read flag on an item.
func (s *Store) MarkRead(id int64, read bool) error {
	_, err := s.db.Exec(`UPDATE items SET is_read = ? WHERE id = ?`, boolToInt(read), id)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// MarkStarred sets or clears the is_starred flag on an item.
func (s *Store) MarkStarred(id int64, starred bool) error {
	_, err := s.db.Exec(`UPDATE items SET is_starred = ? WHERE id = ?`, boolToInt(starred), id)
	if err != nil {
		return fmt.Errorf("mark starred: %w", err)
	}
	return nil
}

// ListItemsBySubscription returns items for a subscription ordered by published_at DESC.
func (s *Store) ListItemsBySubscription(subID int64, limit int) ([]Item, error) {
	rows, err := s.db.Query(`
		SELECT id, subscription_id, guid, type, title, author, url, content_html,
		       media_url, media_mime, duration_sec, thumbnail_url, published_at,
		       fetched_at, is_read, is_starred, content_hash
		FROM items WHERE subscription_id = ?
		ORDER BY published_at DESC
		LIMIT ?`, subID, limit)
	if err != nil {
		return nil, fmt.Errorf("list items by subscription: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var it Item
		var isRead, isStarred int
		if err := rows.Scan(
			&it.ID, &it.SubscriptionID, &it.GUID, &it.Type, &it.Title, &it.Author,
			&it.URL, &it.ContentHTML, &it.MediaURL, &it.MediaMIME, &it.DurationSec,
			&it.ThumbnailURL, &it.PublishedAt, &it.FetchedAt, &isRead, &isStarred,
			&it.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("list items scan: %w", err)
		}
		it.IsRead = isRead != 0
		it.IsStarred = isStarred != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list items rows: %w", err)
	}
	return items, nil
}

// --- Tag methods ---

// AddTag ensures a tag exists and associates it with the item. Idempotent on (item, tag).
func (s *Store) AddTag(itemID int64, tag string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("add tag begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	if _, err = tx.Exec(`INSERT OR IGNORE INTO tags (name) VALUES (?)`, tag); err != nil {
		return fmt.Errorf("add tag insert tag: %w", err)
	}
	var tagID int64
	if err = tx.QueryRow(`SELECT id FROM tags WHERE name = ?`, tag).Scan(&tagID); err != nil {
		return fmt.Errorf("add tag select tag id: %w", err)
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO item_tags (item_id, tag_id) VALUES (?, ?)`, itemID, tagID); err != nil {
		return fmt.Errorf("add tag insert item_tag: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("add tag commit: %w", err)
	}
	return nil
}

// --- Filter type ---

// Filter is an auto-action rule applied to incoming items.
type Filter struct {
	ID          int64
	SortOrder   int64
	Query       string
	Action      string // skip | tag | download | mute_profanity | transcribe
	ActionValue string
	CreatedAt   int64
}

// --- Download type ---

// Download tracks a media file download for an item.
type Download struct {
	ItemID          int64
	State           string // queued | downloading | ready | error
	LocalPath       string
	CleanAudioPath  string
	CaptionsPath    string
	Bytes           int64
	Error           string
	StartedAt       int64
	CompletedAt     int64
	NeedsProfanity  bool
	NeedsTranscribe bool
	SilenceRanges   string // JSON array of Range (action="skip"); empty when unknown
	SilenceState    string // "" = unknown, "done" = computed (may be empty result)
}

// Range is a time span within a media item with an associated player action.
// Action is "mute" (drop volume during the span) or "skip" (seek past it).
type Range struct {
	Start  float64 `json:"start"`
	End    float64 `json:"end"`
	Action string  `json:"action"`
}

// --- Playback type ---

// Playback tracks listen progress for an item.
type Playback struct {
	ItemID       int64
	ProgressSec  float64
	Completed    bool
	LastPlayedAt int64
}

// --- Playlist type ---

// Playlist is a saved query that produces an ordered list of items.
type Playlist struct {
	ID        int64
	Name      string
	Query     string
	SortOrder int64
	CreatedAt int64
}

// --- Filter methods ---

// CreateFilter inserts a new filter and returns its ID.
func (s *Store) CreateFilter(f Filter) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO filters (sort_order, query, action, action_value)
		VALUES (?, ?, ?, ?)`,
		f.SortOrder, f.Query, f.Action, f.ActionValue,
	)
	if err != nil {
		return 0, fmt.Errorf("create filter: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create filter last insert id: %w", err)
	}
	return id, nil
}

// ListFilters returns all filters ordered by sort_order ASC.
func (s *Store) ListFilters() ([]Filter, error) {
	rows, err := s.db.Query(`
		SELECT id, sort_order, query, action, action_value, created_at
		FROM filters ORDER BY sort_order ASC`)
	if err != nil {
		return nil, fmt.Errorf("list filters: %w", err)
	}
	defer rows.Close()
	var filters []Filter
	for rows.Next() {
		var f Filter
		if err := rows.Scan(&f.ID, &f.SortOrder, &f.Query, &f.Action, &f.ActionValue, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("list filters scan: %w", err)
		}
		filters = append(filters, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list filters rows: %w", err)
	}
	return filters, nil
}

// UpdateFilter updates an existing filter.
func (s *Store) UpdateFilter(f Filter) error {
	_, err := s.db.Exec(`
		UPDATE filters SET sort_order = ?, query = ?, action = ?, action_value = ?
		WHERE id = ?`,
		f.SortOrder, f.Query, f.Action, f.ActionValue, f.ID,
	)
	if err != nil {
		return fmt.Errorf("update filter: %w", err)
	}
	return nil
}

// DeleteFilter removes a filter by ID.
func (s *Store) DeleteFilter(id int64) error {
	_, err := s.db.Exec(`DELETE FROM filters WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete filter: %w", err)
	}
	return nil
}

// --- Download methods ---

// EnqueueDownload upserts a download row in state=queued.
// Does not overwrite a row already in state 'ready' or 'downloading'.
func (s *Store) EnqueueDownload(itemID int64) error {
	_, err := s.db.Exec(`
		INSERT INTO downloads (item_id, state) VALUES (?, 'queued')
		ON CONFLICT(item_id) DO UPDATE SET
			state = CASE
				WHEN downloads.state IN ('ready','downloading') THEN downloads.state
				ELSE 'queued'
			END`,
		itemID,
	)
	if err != nil {
		return fmt.Errorf("enqueue download: %w", err)
	}
	return nil
}

// PendingDownloads returns up to limit downloads in state 'queued', ordered by item_id.
func (s *Store) PendingDownloads(limit int) ([]Download, error) {
	rows, err := s.db.Query(`
		SELECT item_id, state, local_path, clean_audio_path, captions_path,
		       bytes, error, started_at, completed_at, needs_profanity, needs_transcribe
		FROM downloads WHERE state = 'queued' ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending downloads: %w", err)
	}
	return scanDownloads(rows)
}

// MarkDownloadStart transitions a download to state 'downloading'.
func (s *Store) MarkDownloadStart(itemID int64) error {
	_, err := s.db.Exec(`
		UPDATE downloads SET state = 'downloading', started_at = unixepoch()
		WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("mark download start: %w", err)
	}
	return nil
}

// MarkDownloadReady transitions a download to state 'ready' with path and byte count.
func (s *Store) MarkDownloadReady(itemID int64, path string, bytes int64) error {
	_, err := s.db.Exec(`
		UPDATE downloads SET state = 'ready', local_path = ?, bytes = ?, completed_at = unixepoch()
		WHERE item_id = ?`, path, bytes, itemID)
	if err != nil {
		return fmt.Errorf("mark download ready: %w", err)
	}
	return nil
}

// MarkDownloadError transitions a download to state 'error' with a message.
func (s *Store) MarkDownloadError(itemID int64, errMsg string) error {
	_, err := s.db.Exec(`
		UPDATE downloads SET state = 'error', error = ?
		WHERE item_id = ?`, errMsg, itemID)
	if err != nil {
		return fmt.Errorf("mark download error: %w", err)
	}
	return nil
}

// SetCleanAudioPath updates the clean_audio_path for a download row.
func (s *Store) SetCleanAudioPath(itemID int64, path string) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_audio_path = ? WHERE item_id = ?`, path, itemID)
	if err != nil {
		return fmt.Errorf("set clean audio path: %w", err)
	}
	return nil
}

// SetCaptionsPath updates the captions_path for a download row.
func (s *Store) SetCaptionsPath(itemID int64, path string) error {
	_, err := s.db.Exec(`UPDATE downloads SET captions_path = ? WHERE item_id = ?`, path, itemID)
	if err != nil {
		return fmt.Errorf("set captions path: %w", err)
	}
	return nil
}

// GetDownload returns the download record for an item.
func (s *Store) GetDownload(itemID int64) (Download, error) {
	var d Download
	var needsProf, needsTrans int
	err := s.db.QueryRow(`
		SELECT item_id, state, local_path, clean_audio_path, captions_path,
		       bytes, error, started_at, completed_at, needs_profanity, needs_transcribe,
		       silence_ranges, silence_state
		FROM downloads WHERE item_id = ?`, itemID).Scan(
		&d.ItemID, &d.State, &d.LocalPath, &d.CleanAudioPath, &d.CaptionsPath,
		&d.Bytes, &d.Error, &d.StartedAt, &d.CompletedAt, &needsProf, &needsTrans,
		&d.SilenceRanges, &d.SilenceState,
	)
	if err != nil {
		return Download{}, fmt.Errorf("get download: %w", err)
	}
	d.NeedsProfanity = needsProf != 0
	d.NeedsTranscribe = needsTrans != 0
	return d, nil
}

// GetSilenceRanges decodes the cached silence ranges for an item.
func (s *Store) GetSilenceRanges(itemID int64) ([]Range, error) {
	d, err := s.GetDownload(itemID)
	if err != nil {
		return nil, err
	}
	if d.SilenceRanges == "" {
		return nil, nil
	}
	var rs []Range
	if err := json.Unmarshal([]byte(d.SilenceRanges), &rs); err != nil {
		return nil, fmt.Errorf("decode silence ranges for item %d: %w", itemID, err)
	}
	return rs, nil
}

// SetSilenceRanges caches silence ranges for an item and marks detection done.
func (s *Store) SetSilenceRanges(itemID int64, ranges []Range) error {
	buf, err := json.Marshal(ranges)
	if err != nil {
		return fmt.Errorf("encode silence ranges: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE downloads SET silence_ranges = ?, silence_state = 'done' WHERE item_id = ?`,
		string(buf), itemID)
	if err != nil {
		return fmt.Errorf("save silence ranges for item %d: %w", itemID, err)
	}
	return nil
}

// EnqueueProfanity ensures a download row exists and sets needs_profanity=1.
func (s *Store) EnqueueProfanity(itemID int64) error {
	if err := s.EnqueueDownload(itemID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE downloads SET needs_profanity = 1 WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("enqueue profanity: %w", err)
	}
	return nil
}

// PendingProfanity returns up to limit item IDs where needs_profanity=1.
func (s *Store) PendingProfanity(limit int) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT item_id FROM downloads WHERE needs_profanity = 1 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending profanity: %w", err)
	}
	return scanInt64IDs(rows, "pending profanity")
}

// PendingProfanityReady returns item IDs that need profanity muting and whose
// download row is already in 'ready' state.
func (s *Store) PendingProfanityReady(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT item_id FROM downloads
		 WHERE needs_profanity = 1 AND state = 'ready'
		 ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending profanity ready: %w", err)
	}
	return scanInt64IDs(rows, "pending profanity ready")
}

// PendingProfanityWithCaptions returns item IDs that need profanity muting,
// have a ready download, AND have a captions file produced by the transcribe
// worker. Without captions the profanity worker cannot identify regions to mute.
func (s *Store) PendingProfanityWithCaptions(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT item_id FROM downloads
		 WHERE needs_profanity = 1 AND state = 'ready' AND captions_path != ''
		 ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending profanity with captions: %w", err)
	}
	defer rows.Close()
	return scanInt64IDs(rows, "pending profanity with captions")
}

// ClearNeedsProfanity clears the needs_profanity flag for a download.
func (s *Store) ClearNeedsProfanity(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET needs_profanity = 0 WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("clear needs profanity: %w", err)
	}
	return nil
}

// EnqueueTranscribe ensures a download row exists and sets needs_transcribe=1.
func (s *Store) EnqueueTranscribe(itemID int64) error {
	if err := s.EnqueueDownload(itemID); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE downloads SET needs_transcribe = 1 WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("enqueue transcribe: %w", err)
	}
	return nil
}

// PendingTranscribe returns up to limit item IDs where needs_transcribe=1.
func (s *Store) PendingTranscribe(limit int) ([]int64, error) {
	rows, err := s.db.Query(`
		SELECT item_id FROM downloads WHERE needs_transcribe = 1 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending transcribe: %w", err)
	}
	return scanInt64IDs(rows, "pending transcribe")
}

// PendingTranscribeReady returns item IDs that need transcription and whose
// download row is already in 'ready' state.
func (s *Store) PendingTranscribeReady(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT item_id FROM downloads
		 WHERE needs_transcribe = 1 AND state = 'ready'
		 ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending transcribe ready: %w", err)
	}
	return scanInt64IDs(rows, "pending transcribe ready")
}

// ClearNeedsTranscribe clears the needs_transcribe flag for a download.
func (s *Store) ClearNeedsTranscribe(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET needs_transcribe = 0 WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("clear needs transcribe: %w", err)
	}
	return nil
}

// scanDownloads reads download rows into a slice.
func scanDownloads(rows *sql.Rows) ([]Download, error) {
	defer rows.Close()
	var downloads []Download
	for rows.Next() {
		var d Download
		var needsProf, needsTrans int
		if err := rows.Scan(
			&d.ItemID, &d.State, &d.LocalPath, &d.CleanAudioPath, &d.CaptionsPath,
			&d.Bytes, &d.Error, &d.StartedAt, &d.CompletedAt, &needsProf, &needsTrans,
		); err != nil {
			return nil, fmt.Errorf("scan download: %w", err)
		}
		d.NeedsProfanity = needsProf != 0
		d.NeedsTranscribe = needsTrans != 0
		downloads = append(downloads, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan downloads rows: %w", err)
	}
	return downloads, nil
}

// scanInt64IDs reads a single-column int64 result into a slice.
func scanInt64IDs(rows *sql.Rows, ctx string) ([]int64, error) {
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%s scan: %w", ctx, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%s rows: %w", ctx, err)
	}
	return ids, nil
}

// --- Playback methods ---

// SetPlayback upserts playback progress for an item.
func (s *Store) SetPlayback(itemID int64, progressSec float64, completed bool) error {
	_, err := s.db.Exec(`
		INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
		VALUES (?, ?, ?, unixepoch())
		ON CONFLICT(item_id) DO UPDATE SET
			progress_sec = excluded.progress_sec,
			completed = excluded.completed,
			last_played_at = unixepoch()`,
		itemID, progressSec, boolToInt(completed),
	)
	if err != nil {
		return fmt.Errorf("set playback: %w", err)
	}
	return nil
}

// GetPlayback returns playback progress for an item.
func (s *Store) GetPlayback(itemID int64) (Playback, error) {
	var pb Playback
	var completed int
	err := s.db.QueryRow(`
		SELECT item_id, progress_sec, completed, last_played_at
		FROM playback WHERE item_id = ?`, itemID).Scan(
		&pb.ItemID, &pb.ProgressSec, &completed, &pb.LastPlayedAt,
	)
	if err != nil {
		return Playback{}, fmt.Errorf("get playback: %w", err)
	}
	pb.Completed = completed != 0
	return pb, nil
}

// ItemPlayback is an Item joined with subscription, playback progress, and download info.
type ItemPlayback struct {
	Item
	SubscriptionTitle string
	ProgressSec       float64
	Completed         bool
	DownloadState     string
	HasCleanAudio     bool
	SourceKind        string // subscription.kind
}

// GetItemPlayback returns an ItemPlayback for a single item by ID, joining
// subscriptions, playback, and downloads. Missing playback/download rows are
// treated as zero values (no progress, not downloaded).
func (s *Store) GetItemPlayback(itemID int64) (ItemPlayback, error) {
	var ip ItemPlayback
	var isRead, isStarred int
	var progressSec float64
	var completed, hasClean int
	var downloadState string

	err := s.db.QueryRow(`
		SELECT
			i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
			i.content_html, i.media_url, i.media_mime, i.duration_sec, i.thumbnail_url,
			i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
			COALESCE(s.title, s.url) AS subscription_title,
			s.kind,
			COALESCE(pb.progress_sec, 0.0),
			COALESCE(pb.completed, 0),
			COALESCE(d.state, ''),
			CASE WHEN d.clean_audio_path != '' AND d.clean_audio_path IS NOT NULL THEN 1 ELSE 0 END
		FROM items i
		JOIN subscriptions s ON i.subscription_id = s.id
		LEFT JOIN playback pb ON pb.item_id = i.id
		LEFT JOIN downloads d ON d.item_id = i.id
		WHERE i.id = ?`, itemID).Scan(
		&ip.ID, &ip.SubscriptionID, &ip.GUID, &ip.Type, &ip.Title, &ip.Author, &ip.URL,
		&ip.ContentHTML, &ip.MediaURL, &ip.MediaMIME, &ip.DurationSec, &ip.ThumbnailURL,
		&ip.PublishedAt, &ip.FetchedAt, &isRead, &isStarred, &ip.ContentHash,
		&ip.SubscriptionTitle,
		&ip.SourceKind,
		&progressSec,
		&completed,
		&downloadState,
		&hasClean,
	)
	if err != nil {
		return ItemPlayback{}, fmt.Errorf("get item playback: %w", err)
	}
	ip.IsRead = isRead != 0
	ip.IsStarred = isStarred != 0
	ip.ProgressSec = progressSec
	ip.Completed = completed != 0
	ip.DownloadState = downloadState
	ip.HasCleanAudio = hasClean != 0
	return ip, nil
}

// --- Playlist methods ---

// CreatePlaylist inserts a new playlist and returns its ID.
func (s *Store) CreatePlaylist(name, query string) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO playlists (name, query) VALUES (?, ?)`, name, query)
	if err != nil {
		return 0, fmt.Errorf("create playlist: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create playlist last insert id: %w", err)
	}
	return id, nil
}

// ListPlaylists returns all playlists ordered by sort_order, name.
func (s *Store) ListPlaylists() ([]Playlist, error) {
	rows, err := s.db.Query(`
		SELECT id, name, query, sort_order, created_at
		FROM playlists ORDER BY sort_order, name`)
	if err != nil {
		return nil, fmt.Errorf("list playlists: %w", err)
	}
	defer rows.Close()
	var playlists []Playlist
	for rows.Next() {
		var p Playlist
		if err := rows.Scan(&p.ID, &p.Name, &p.Query, &p.SortOrder, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("list playlists scan: %w", err)
		}
		playlists = append(playlists, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list playlists rows: %w", err)
	}
	return playlists, nil
}

// UpdatePlaylist updates an existing playlist.
func (s *Store) UpdatePlaylist(p Playlist) error {
	_, err := s.db.Exec(`
		UPDATE playlists SET name = ?, query = ?, sort_order = ?
		WHERE id = ?`,
		p.Name, p.Query, p.SortOrder, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update playlist: %w", err)
	}
	return nil
}

// GetPlaylist returns a single playlist by ID.
func (s *Store) GetPlaylist(id int64) (Playlist, error) {
	var p Playlist
	row := s.db.QueryRow(`SELECT id, name, query, sort_order, created_at FROM playlists WHERE id = ?`, id)
	if err := row.Scan(&p.ID, &p.Name, &p.Query, &p.SortOrder, &p.CreatedAt); err != nil {
		return p, fmt.Errorf("get playlist: %w", err)
	}
	return p, nil
}

// DeletePlaylist removes a playlist by ID.
func (s *Store) DeletePlaylist(id int64) error {
	_, err := s.db.Exec(`DELETE FROM playlists WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete playlist: %w", err)
	}
	return nil
}

// CountUnread returns the number of unread items across all subscriptions.
func (s *Store) CountUnread() (int64, error) {
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_read = 0`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

// SidebarCounts aggregates the sums buildSidebar needs in one pass.
type SidebarCounts struct {
	TotalUnread    int
	StarredCount   int
	BySubscription map[int64]int
	ByFolder       map[int64]int
}

// LoadSidebarCounts returns per-subscription unread sums (and the derived
// per-folder / total sums) plus a starred count, in one SQL pass + one
// scalar query.
func (s *Store) LoadSidebarCounts() (SidebarCounts, error) {
	sc := SidebarCounts{BySubscription: map[int64]int{}, ByFolder: map[int64]int{}}
	rows, err := s.db.Query(`
		SELECT s.id, s.folder_id,
		       COALESCE(SUM(CASE WHEN i.is_read = 0 THEN 1 ELSE 0 END), 0) AS unread
		FROM subscriptions s
		LEFT JOIN items i ON i.subscription_id = s.id
		GROUP BY s.id, s.folder_id`)
	if err != nil {
		return sc, fmt.Errorf("sidebar counts subs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var sid int64
		var fid sql.NullInt64
		var unread int
		if err := rows.Scan(&sid, &fid, &unread); err != nil {
			return sc, fmt.Errorf("sidebar counts scan: %w", err)
		}
		sc.BySubscription[sid] = unread
		sc.TotalUnread += unread
		if fid.Valid {
			sc.ByFolder[fid.Int64] += unread
		}
	}
	if err := rows.Err(); err != nil {
		return sc, fmt.Errorf("sidebar counts rows: %w", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_starred = 1`).Scan(&sc.StarredCount); err != nil {
		return sc, fmt.Errorf("sidebar starred count: %w", err)
	}
	return sc, nil
}

// ItemWithSubscription is an Item joined with its subscription title.
type ItemWithSubscription struct {
	Item
	SubscriptionTitle string
}

// ViewSpec selects which items to return in ListItemsForView.
type ViewSpec struct {
	All            bool
	Starred        bool
	FolderID       sql.NullInt64
	SubscriptionID sql.NullInt64
	TagName        string
	HideRead       bool
	Query          query.Expr    // optional cog-grammar filter; compiled to SQL.
	Schema         *query.Schema // required when Query != nil.
}

// ListItemsForView returns items (with subscription title) according to the ViewSpec.
func (s *Store) ListItemsForView(spec ViewSpec, limit int) ([]ItemWithSubscription, error) {
	base := `
		SELECT i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
		       i.content_html, i.media_url, i.media_mime, i.duration_sec, i.thumbnail_url,
		       i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		       COALESCE(s.title, s.url) AS subscription_title
		FROM items i
		JOIN subscriptions s ON i.subscription_id = s.id`

	var where []string
	var args []any

	switch {
	case spec.Starred:
		where = append(where, "i.is_starred = 1")
	case spec.SubscriptionID.Valid:
		where = append(where, "i.subscription_id = ?")
		args = append(args, spec.SubscriptionID.Int64)
	case spec.FolderID.Valid:
		where = append(where, "s.folder_id = ?")
		args = append(args, spec.FolderID.Int64)
	case spec.TagName != "":
		base += " JOIN item_tags it ON it.item_id = i.id JOIN tags t ON t.id = it.tag_id"
		where = append(where, "t.name = ?")
		args = append(args, spec.TagName)
	}
	// spec.All: no extra WHERE

	if spec.HideRead {
		where = append(where, "i.is_read = 0")
	}

	if spec.Query != nil {
		if spec.Schema == nil {
			return nil, fmt.Errorf("list items for view: Query set but Schema is nil")
		}
		w, qArgs, joinNames, err := query.Compile(spec.Query, *spec.Schema)
		if err != nil {
			return nil, fmt.Errorf("list items for view: compile: %w", err)
		}
		for _, name := range joinNames {
			base += " " + spec.Schema.Joins[name]
		}
		if w != "" {
			where = append(where, w)
			args = append(args, qArgs...)
		}
	}

	q := base
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY i.published_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list items for view: %w", err)
	}
	defer rows.Close()

	var items []ItemWithSubscription
	for rows.Next() {
		var iws ItemWithSubscription
		var isRead, isStarred int
		if err := rows.Scan(
			&iws.ID, &iws.SubscriptionID, &iws.GUID, &iws.Type, &iws.Title, &iws.Author,
			&iws.URL, &iws.ContentHTML, &iws.MediaURL, &iws.MediaMIME, &iws.DurationSec,
			&iws.ThumbnailURL, &iws.PublishedAt, &iws.FetchedAt, &isRead, &isStarred,
			&iws.ContentHash, &iws.SubscriptionTitle,
		); err != nil {
			return nil, fmt.Errorf("list items for view scan: %w", err)
		}
		iws.IsRead = isRead != 0
		iws.IsStarred = isStarred != 0
		items = append(items, iws)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list items for view rows: %w", err)
	}
	return items, nil
}

// MarkAllRead marks all items as read.
func (s *Store) MarkAllRead() error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1`)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// MarkAllReadBySubscription marks all items in a subscription as read.
func (s *Store) MarkAllReadBySubscription(subID int64) error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1 WHERE subscription_id = ?`, subID)
	if err != nil {
		return fmt.Errorf("mark all read by subscription: %w", err)
	}
	return nil
}

// MarkAllReadByFolder marks all items in subscriptions belonging to a folder as read.
func (s *Store) MarkAllReadByFolder(folderID int64) error {
	_, err := s.db.Exec(`
		UPDATE items SET is_read = 1
		WHERE subscription_id IN (SELECT id FROM subscriptions WHERE folder_id = ?)`, folderID)
	if err != nil {
		return fmt.Errorf("mark all read by folder: %w", err)
	}
	return nil
}

// MarkItemRead force-sets a single item as read (non-toggle).
func (s *Store) MarkItemRead(id int64) error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("mark item read: %w", err)
	}
	return nil
}

// RecentUnread returns up to limit unread items ordered by published_at DESC.
func (s *Store) RecentUnread(limit int) ([]Item, error) {
	rows, err := s.db.Query(
		`SELECT id, subscription_id, guid, type, title, author, url, content_html,
		        media_url, media_mime, duration_sec, thumbnail_url, published_at,
		        fetched_at, is_read, is_starred, content_hash
		 FROM items WHERE is_read = 0 ORDER BY published_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("recent unread: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var it Item
		var isRead, isStarred int
		if err := rows.Scan(
			&it.ID, &it.SubscriptionID, &it.GUID, &it.Type, &it.Title, &it.Author,
			&it.URL, &it.ContentHTML, &it.MediaURL, &it.MediaMIME, &it.DurationSec,
			&it.ThumbnailURL, &it.PublishedAt, &it.FetchedAt, &isRead, &isStarred,
			&it.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("recent unread scan: %w", err)
		}
		it.IsRead = isRead != 0
		it.IsStarred = isStarred != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("recent unread rows: %w", err)
	}
	return items, nil
}

// ListItems returns up to limit items, newest first.
func (s *Store) ListItems(limit int) ([]Item, error) {
	rows, err := s.db.Query(
		`SELECT id, subscription_id, guid, type, title, author, url, content_html,
		        media_url, media_mime, duration_sec, thumbnail_url, published_at,
		        fetched_at, is_read, is_starred, content_hash
		 FROM items ORDER BY published_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list items: %w", err)
	}
	defer rows.Close()
	var items []Item
	for rows.Next() {
		var it Item
		var isRead, isStarred int
		if err := rows.Scan(
			&it.ID, &it.SubscriptionID, &it.GUID, &it.Type, &it.Title, &it.Author,
			&it.URL, &it.ContentHTML, &it.MediaURL, &it.MediaMIME, &it.DurationSec,
			&it.ThumbnailURL, &it.PublishedAt, &it.FetchedAt, &isRead, &isStarred,
			&it.ContentHash,
		); err != nil {
			return nil, fmt.Errorf("list items scan: %w", err)
		}
		it.IsRead = isRead != 0
		it.IsStarred = isStarred != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list items rows: %w", err)
	}
	return items, nil
}

// ListTags returns all distinct tag names ordered alphabetically.
func (s *Store) ListTags() ([]string, error) {
	rows, err := s.db.Query(`SELECT name FROM tags ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// RemoveTag removes the association between an item and a tag.
func (s *Store) RemoveTag(itemID int64, tag string) error {
	_, err := s.db.Exec(`
		DELETE FROM item_tags
		WHERE item_id = ? AND tag_id = (SELECT id FROM tags WHERE name = ?)`,
		itemID, tag)
	if err != nil {
		return fmt.Errorf("remove tag: %w", err)
	}
	return nil
}

// ItemTags returns all tag names for an item, ordered by name.
func (s *Store) ItemTags(itemID int64) ([]string, error) {
	rows, err := s.db.Query(`
		SELECT t.name FROM tags t
		JOIN item_tags it ON it.tag_id = t.id
		WHERE it.item_id = ?
		ORDER BY t.name`, itemID)
	if err != nil {
		return nil, fmt.Errorf("item tags: %w", err)
	}
	defer rows.Close()
	var tags []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("item tags scan: %w", err)
		}
		tags = append(tags, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("item tags rows: %w", err)
	}
	return tags, nil
}

// DB returns the underlying database handle.
func (s *Store) DB() *sql.DB { return s.db }

// --- Settings ---

// GetSetting returns a setting value from feed_settings, or the default.
func (s *Store) GetSetting(key, defaultValue string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM feed_settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return defaultValue
	}
	return v
}

// SetSetting upserts a setting value into feed_settings.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO feed_settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set setting %s: %w", key, err)
	}
	return nil
}

// --- Typed settings (mirrors plugin/gallery/store.go pattern) ---

// Setting is a typed key/value setting backed by the feed_settings table.
type Setting[T any] struct {
	Key          string
	DefaultValue T
	Parse        func(s string) (T, error)
	Stringify    func(v T) string
}

// Get reads the setting value from the database, returning the default if absent.
func (s *Setting[T]) Get(db *sql.DB) (T, error) {
	var zero T
	var value string
	err := db.QueryRow(`SELECT value FROM feed_settings WHERE key = ?`, s.Key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return s.DefaultValue, nil
	}
	if err != nil {
		return zero, err
	}
	if value == "" {
		return s.DefaultValue, nil
	}
	return s.Parse(value)
}

// Set writes the setting value to the database.
func (s *Setting[T]) Set(db *sql.DB, value T) error {
	str := fmt.Sprint(value)
	if s.Stringify != nil {
		str = s.Stringify(value)
	}
	_, err := db.Exec(
		`INSERT INTO feed_settings(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, s.Key, str)
	return err
}

// MustGet reads the setting value, returning the default on error.
func (s *Setting[T]) MustGet(db *sql.DB) T {
	v, err := s.Get(db)
	if err != nil {
		return s.DefaultValue
	}
	return v
}

// StringSetting creates a Setting for a string-based type.
func StringSetting[T ~string](key string, defaultValue T) Setting[T] {
	return Setting[T]{
		Key: key, DefaultValue: defaultValue,
		Parse:     func(s string) (T, error) { return T(s), nil },
		Stringify: func(v T) string { return string(v) },
	}
}

// IntSetting creates a Setting for an int value.
func IntSetting(key string, defaultValue int) Setting[int] {
	return Setting[int]{
		Key: key, DefaultValue: defaultValue,
		Parse:     strconv.Atoi,
		Stringify: strconv.Itoa,
	}
}

// BoolSetting creates a Setting for a bool value.
func BoolSetting(key string, defaultValue bool) Setting[bool] {
	return Setting[bool]{
		Key: key, DefaultValue: defaultValue,
		Parse: func(s string) (bool, error) { return s == "true", nil },
		Stringify: func(v bool) string {
			if v {
				return "true"
			}
			return "false"
		},
	}
}

// FloatSetting creates a Setting for a float64 value.
func FloatSetting(key string, defaultValue float64) Setting[float64] {
	return Setting[float64]{
		Key: key, DefaultValue: defaultValue,
		Parse:     func(s string) (float64, error) { return strconv.ParseFloat(s, 64) },
		Stringify: func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) },
	}
}

// Per-user player preferences (stored in feed_settings).
var (
	settingPlaybackSpeed = FloatSetting("playback_speed", 1.0)
	settingSkipBackSec   = IntSetting("skip_back_sec", 15)
	settingSkipFwdSec    = IntSetting("skip_fwd_sec", 30)
	settingVolume        = FloatSetting("volume", 1.0)
	settingAutoplayNext  = BoolSetting("autoplay_next", true)
	settingSkipSilence   = BoolSetting("skip_silence", false)
	settingHideEmpty     = BoolSetting("hide_empty", false)
)

// PlayerPrefs is the per-user set of media-player preferences.
type PlayerPrefs struct {
	Speed        float64 `json:"speed"`
	SkipBackSec  int     `json:"skipBackSec"`
	SkipFwdSec   int     `json:"skipFwdSec"`
	Volume       float64 `json:"volume"`
	AutoplayNext bool    `json:"autoplayNext"`
	SkipSilence  bool    `json:"skipSilence"`
	HideEmpty    bool    `json:"hideEmpty"`
}

// LoadPlayerPrefs reads the per-user player preferences, falling back to defaults.
func (s *Store) LoadPlayerPrefs() (PlayerPrefs, error) {
	var p PlayerPrefs
	var err error
	if p.Speed, err = settingPlaybackSpeed.Get(s.db); err != nil {
		return p, fmt.Errorf("load playback_speed: %w", err)
	}
	if p.SkipBackSec, err = settingSkipBackSec.Get(s.db); err != nil {
		return p, fmt.Errorf("load skip_back_sec: %w", err)
	}
	if p.SkipFwdSec, err = settingSkipFwdSec.Get(s.db); err != nil {
		return p, fmt.Errorf("load skip_fwd_sec: %w", err)
	}
	if p.Volume, err = settingVolume.Get(s.db); err != nil {
		return p, fmt.Errorf("load volume: %w", err)
	}
	if p.AutoplayNext, err = settingAutoplayNext.Get(s.db); err != nil {
		return p, fmt.Errorf("load autoplay_next: %w", err)
	}
	if p.SkipSilence, err = settingSkipSilence.Get(s.db); err != nil {
		return p, fmt.Errorf("load skip_silence: %w", err)
	}
	if p.HideEmpty, err = settingHideEmpty.Get(s.db); err != nil {
		return p, fmt.Errorf("load hide_empty: %w", err)
	}
	return p, nil
}

// SavePlayerPrefs persists the per-user player preferences.
func (s *Store) SavePlayerPrefs(p PlayerPrefs) error {
	if err := settingPlaybackSpeed.Set(s.db, p.Speed); err != nil {
		return fmt.Errorf("save playback_speed: %w", err)
	}
	if err := settingSkipBackSec.Set(s.db, p.SkipBackSec); err != nil {
		return fmt.Errorf("save skip_back_sec: %w", err)
	}
	if err := settingSkipFwdSec.Set(s.db, p.SkipFwdSec); err != nil {
		return fmt.Errorf("save skip_fwd_sec: %w", err)
	}
	if err := settingVolume.Set(s.db, p.Volume); err != nil {
		return fmt.Errorf("save volume: %w", err)
	}
	if err := settingAutoplayNext.Set(s.db, p.AutoplayNext); err != nil {
		return fmt.Errorf("save autoplay_next: %w", err)
	}
	if err := settingSkipSilence.Set(s.db, p.SkipSilence); err != nil {
		return fmt.Errorf("save skip_silence: %w", err)
	}
	if err := settingHideEmpty.Set(s.db, p.HideEmpty); err != nil {
		return fmt.Errorf("save hide_empty: %w", err)
	}
	return nil
}

// --- Whisper model settings ---

// WhisperModel identifies which whisper.cpp model the transcribe worker uses.
type WhisperModel string

const (
	WhisperNone     WhisperModel = "none"
	WhisperTinyEn   WhisperModel = "tiny.en"
	WhisperBaseEn   WhisperModel = "base.en"
	WhisperSmallEn  WhisperModel = "small.en"
	WhisperMediumEn WhisperModel = "medium.en"
	WhisperLargeV3  WhisperModel = "large-v3"
)

var (
	SettingWhisperModel     = StringSetting("whisper_model", WhisperBaseEn)
	SettingWhisperModelsDir = StringSetting("whisper_models_dir", "") // empty means "auto: <plugin data>/whisper-models"
	SettingWhisperCLIPath   = StringSetting("whisper_cli_path", "")   // empty means "auto: lookup whisper-cli in PATH"
)

// --- Playback history ---

// PlayedItem is an Item joined with subscription and playback for history display.
type PlayedItem struct {
	Item
	SubscriptionTitle string
	ProgressSec       float64
	Completed         bool
	LastPlayedAt      int64
}

// ListPlaybackHistory returns items with playback records, newest first.
func (s *Store) ListPlaybackHistory(limit int) ([]PlayedItem, error) {
	rows, err := s.db.Query(
		`SELECT i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
		        i.content_html, i.media_url, i.media_mime, i.duration_sec,
		        i.thumbnail_url, i.published_at, i.fetched_at, i.is_read, i.is_starred,
		        i.content_hash,
		        COALESCE(sub.title, sub.url),
		        pb.progress_sec, pb.completed, pb.last_played_at
		 FROM playback pb
		 JOIN items i ON i.id = pb.item_id
		 JOIN subscriptions sub ON sub.id = i.subscription_id
		 WHERE pb.last_played_at > 0
		 ORDER BY pb.last_played_at DESC
		 LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list playback history: %w", err)
	}
	defer rows.Close()
	var out []PlayedItem
	for rows.Next() {
		var pi PlayedItem
		var isRead, isStarred, completed int
		if err := rows.Scan(
			&pi.ID, &pi.SubscriptionID, &pi.GUID, &pi.Type, &pi.Title, &pi.Author, &pi.URL,
			&pi.ContentHTML, &pi.MediaURL, &pi.MediaMIME, &pi.DurationSec,
			&pi.ThumbnailURL, &pi.PublishedAt, &pi.FetchedAt, &isRead, &isStarred,
			&pi.ContentHash, &pi.SubscriptionTitle,
			&pi.ProgressSec, &completed, &pi.LastPlayedAt,
		); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		pi.IsRead = isRead != 0
		pi.IsStarred = isStarred != 0
		pi.Completed = completed != 0
		out = append(out, pi)
	}
	return out, rows.Err()
}

// PlaybackHistoryItems returns played items joined with their subscription
// title, ordered by last-played descending. Same shape as ListItemsForView's
// return, so callers that need the view shape don't need to remap from
// PlayedItem.
func (s *Store) PlaybackHistoryItems(limit int) ([]ItemWithSubscription, error) {
	rows, err := s.db.Query(`
		SELECT i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
		       i.content_html, i.media_url, i.media_mime, i.duration_sec,
		       i.thumbnail_url, i.published_at, i.fetched_at, i.is_read, i.is_starred,
		       i.content_hash,
		       COALESCE(s.title, s.url) AS subscription_title
		FROM playback p
		JOIN items i ON i.id = p.item_id
		JOIN subscriptions s ON s.id = i.subscription_id
		ORDER BY p.last_played_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("playback history items: %w", err)
	}
	defer rows.Close()
	var out []ItemWithSubscription
	for rows.Next() {
		var iws ItemWithSubscription
		var isRead, isStarred int
		if err := rows.Scan(
			&iws.ID, &iws.SubscriptionID, &iws.GUID, &iws.Type, &iws.Title, &iws.Author,
			&iws.URL, &iws.ContentHTML, &iws.MediaURL, &iws.MediaMIME, &iws.DurationSec,
			&iws.ThumbnailURL, &iws.PublishedAt, &iws.FetchedAt, &isRead, &isStarred,
			&iws.ContentHash, &iws.SubscriptionTitle,
		); err != nil {
			return nil, fmt.Errorf("playback history items scan: %w", err)
		}
		iws.IsRead = isRead != 0
		iws.IsStarred = isStarred != 0
		out = append(out, iws)
	}
	return out, rows.Err()
}

// --- Per-subscription settings store helpers ---

// UpdateSubscriptionPollInterval updates the poll_interval_sec for a subscription.
func (s *Store) UpdateSubscriptionPollInterval(id, sec int64) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET poll_interval_sec = ? WHERE id = ?`, sec, id)
	if err != nil {
		return fmt.Errorf("update poll interval: %w", err)
	}
	return nil
}

// UpdateSubscriptionFolder sets the folder for a subscription (NULL clears it).
func (s *Store) UpdateSubscriptionFolder(id int64, folderID sql.NullInt64) error {
	_, err := s.db.Exec(`UPDATE subscriptions SET folder_id = ? WHERE id = ?`, folderID, id)
	if err != nil {
		return fmt.Errorf("update folder: %w", err)
	}
	return nil
}
