package feed

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
		FROM subscriptions ORDER BY title`)
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
		       bytes, error, started_at, completed_at, needs_profanity, needs_transcribe
		FROM downloads WHERE item_id = ?`, itemID).Scan(
		&d.ItemID, &d.State, &d.LocalPath, &d.CleanAudioPath, &d.CaptionsPath,
		&d.Bytes, &d.Error, &d.StartedAt, &d.CompletedAt, &needsProf, &needsTrans,
	)
	if err != nil {
		return Download{}, fmt.Errorf("get download: %w", err)
	}
	d.NeedsProfanity = needsProf != 0
	d.NeedsTranscribe = needsTrans != 0
	return d, nil
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
