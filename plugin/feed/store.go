package feed

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

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
	rows, err := s.db.Query(`SELECT id, name, created_at FROM folders ORDER BY name, id`)
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

// updatedOne wraps an UPDATE's error, and reports sql.ErrNoRows if it
// changed no row, so a caller can tell a missing row from a failure.
func updatedOne(res sql.Result, err error, format string, args ...any) error {
	if err != nil {
		return fmt.Errorf(format+": %w", append(args, err)...)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf(format+" rows affected: %w", append(args, err)...)
	}
	if n == 0 {
		return fmt.Errorf(format+": %w", append(args, sql.ErrNoRows)...)
	}
	return nil
}

// RenameFolder updates a folder's name, or returns sql.ErrNoRows if there is
// none.
func (s *Store) RenameFolder(id int64, name string) error {
	res, err := s.db.Exec(`UPDATE folders SET name = ? WHERE id = ?`, name, id)
	return updatedOne(res, err, "rename folder %d", id)
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
		FROM subscriptions ORDER BY (CASE WHEN error_count > 0 THEN 0 ELSE 1 END) ASC, LOWER(COALESCE(NULLIF(title, ''), url)) ASC, id ASC`)
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
			FROM subscriptions WHERE folder_id = ? ORDER BY title, id`, folderID.Int64)
	} else {
		rows, err = s.db.Query(`
			SELECT id, folder_id, kind, url, title, description, site_url, image_url,
			       favicon_url, etag, last_modified, poll_interval_sec, next_check_at,
			       last_checked_at, last_success_at, error_count, last_error, paused, created_at
			FROM subscriptions WHERE folder_id IS NULL ORDER BY title, id`)
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
		if key == "title" {
			// A title the user gave it stays.
			setClauses = append(setClauses, "title = CASE WHEN title_locked THEN title ELSE ? END")
		} else {
			setClauses = append(setClauses, key+" = ?")
		}
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
		INSERT INTO items
			(subscription_id, guid, type, title, author, url, content_html,
			 media_url, media_mime, duration_sec, thumbnail_url, published_at,
			 is_read, is_starred, content_hash)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(subscription_id, guid) DO NOTHING`,
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
	_, err := s.db.Exec(`UPDATE items SET is_read = ?, fetched_at = unixepoch() WHERE id = ?`, boolToInt(read), id)
	if err != nil {
		return fmt.Errorf("mark read: %w", err)
	}
	return nil
}

// MarkStarred sets or clears the is_starred flag on an item.
func (s *Store) MarkStarred(id int64, starred bool) error {
	_, err := s.db.Exec(`UPDATE items SET is_starred = ?, fetched_at = unixepoch() WHERE id = ?`, boolToInt(starred), id)
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
		ORDER BY published_at DESC, id DESC
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
	VideoPath       string // the video track, as downloaded or as packaged
	PackageState    string // "" pending, "done", "error" (pipeline/package.go)
	PackageError    string
	CleanTrackPath  string // the clean audio as an HLS track; "" until packaged
	CleanTrackError string
	EdgesWanted     bool   // a player asked for the skip presentation
	EdgesEdit       string // the edit list the edges were made for; "" = none yet
	EdgesError      string // why they could not be made
	EdgesIndex      string // JSON EdgesIndex of the edges of EdgesEdit; "" until made
	ProfanityError  string // why profanity muting last failed; "" if it has not
	TranscribeError string // why transcription last failed; "" if it has not
}

// muted is whether the item's audio is to be muted: a mute filter matched it
// (EnqueueProfanity), muting is pending or failed, or a clean file exists.
// Such an item plays only its clean audio, never one it was not made from.
func (d Download) muted() bool {
	return d.NeedsProfanity || d.CleanAudioPath != ""
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

// Playlist is a saved query that produces an ordered list of items. How it
// plays — its order and whether videos play as audio — is the same on every
// device; what a device downloads of it is the device's business.
type Playlist struct {
	ID        int64
	Name      string
	Query     string
	SortOrder int64 // its place in the sidebar
	CreatedAt int64
	Order     PlayOrder
	// ShuffleSeed holds a shuffled order still until it is reshuffled.
	ShuffleSeed int64
	// ListenOnly plays (and downloads) its videos as audio.
	ListenOnly bool
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
		FROM filters ORDER BY sort_order ASC, id ASC`)
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

// UpdateFilter updates an existing filter, or returns sql.ErrNoRows if there
// is none.
func (s *Store) UpdateFilter(f Filter) error {
	res, err := s.db.Exec(`
		UPDATE filters SET sort_order = ?, query = ?, action = ?, action_value = ?
		WHERE id = ?`,
		f.SortOrder, f.Query, f.Action, f.ActionValue, f.ID,
	)
	return updatedOne(res, err, "update filter %d", f.ID)
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

const enqueueDownloadSQL = `
	INSERT INTO downloads (item_id, state) VALUES (?, 'queued')
	ON CONFLICT(item_id) DO UPDATE SET
		state = CASE
			WHEN downloads.state = 'ready' AND downloads.package_state = 'error' AND downloads.video_path != '' THEN 'queued'
			WHEN downloads.state IN ('ready','downloading') THEN downloads.state
			ELSE 'queued'
		END,
		error = CASE
			WHEN downloads.state = 'ready' AND downloads.package_state = 'error' AND downloads.video_path != '' THEN ''
			WHEN downloads.state IN ('ready','downloading') THEN downloads.error
			ELSE ''
		END`

// ErrNotTalksPage is returned by AddPageTalks when the page's URL is already
// a subscription of another sort, which its talks must not be mixed into.
var ErrNotTalksPage = errors.New("already subscribed to this address")

// AddPageTalks files talks found on a page of talks under a subscription for
// that page, creating it if needed, and queues each talk for download. The
// subscription is a paused "url" one: its items come from the page's links,
// not from polling the page, which would add the page itself as an article.
// Adding a page again adds the talks it lacks and retries failed downloads.
// Talks are dated from now down in page order, so lists show them as the page
// does. It returns the subscription and how many talks were new.
func (s *Store) AddPageTalks(pageURL, title string, folderID sql.NullInt64, talks []PageTalk, now int64) (int64, int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("add page talks begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`
		INSERT INTO subscriptions (folder_id, kind, url, title, site_url, paused)
		VALUES (?, 'url', ?, ?, ?, 1)
		ON CONFLICT(url) DO NOTHING`,
		folderID, pageURL, title, pageURL); err != nil {
		return 0, 0, fmt.Errorf("add page talks subscription: %w", err)
	}
	var subID int64
	var kind string
	var paused bool
	if err := tx.QueryRow(`SELECT id, kind, paused FROM subscriptions WHERE url = ?`, pageURL).
		Scan(&subID, &kind, &paused); err != nil {
		return 0, 0, fmt.Errorf("add page talks find subscription: %w", err)
	}
	if kind != "url" || !paused {
		return 0, 0, ErrNotTalksPage
	}

	insert, err := tx.Prepare(`
		INSERT INTO items (subscription_id, guid, type, title, author, url, media_url, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(subscription_id, guid) DO NOTHING
		RETURNING id`)
	if err != nil {
		return 0, 0, fmt.Errorf("add page talks prepare: %w", err)
	}
	defer insert.Close()

	added := 0
	for i, t := range talks {
		var id int64
		err := insert.QueryRow(subID, t.PageURL, t.Type, t.Title, t.Speaker, t.PageURL, t.MediaURL, now-int64(i)).Scan(&id)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if err := tx.QueryRow(`SELECT id FROM items WHERE subscription_id = ? AND guid = ?`, subID, t.PageURL).Scan(&id); err != nil {
				return 0, 0, fmt.Errorf("add page talks find %s: %w", t.PageURL, err)
			}
		case err != nil:
			return 0, 0, fmt.Errorf("add page talks insert %s: %w", t.PageURL, err)
		default:
			added++
		}
		if _, err := tx.Exec(enqueueDownloadSQL, id); err != nil {
			return 0, 0, fmt.Errorf("add page talks enqueue %s: %w", t.PageURL, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("add page talks commit: %w", err)
	}
	return subID, added, nil
}

// EnqueueDownload upserts a download row in state=queued.
// Does not overwrite a row already in state 'ready' or 'downloading'; a row
// that failed is queued again and its error cleared, which is a retry.
func (s *Store) EnqueueDownload(itemID int64) error {
	_, err := s.db.Exec(enqueueDownloadSQL, itemID)
	if err != nil {
		return fmt.Errorf("enqueue download: %w", err)
	}
	return nil
}

// PendingDownloads returns up to limit downloads in state 'queued', ordered by item_id.
func (s *Store) PendingDownloads(limit int) ([]Download, error) {
	rows, err := s.db.Query(`
		SELECT item_id, state, local_path, clean_audio_path, captions_path,
		       bytes, error, started_at, completed_at, needs_profanity, needs_transcribe,
		       video_path, package_state, package_error, clean_track_path, clean_track_error
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

// MarkDownloadReady transitions a download to state 'ready' with path and
// byte count. The packager looks at it again.
func (s *Store) MarkDownloadReady(itemID int64, path string, bytes int64) error {
	return s.markReady(itemID, path, "", bytes)
}

// MarkVideoDownloadReady is MarkDownloadReady for a download that came as
// an audio and a video file.
func (s *Store) MarkVideoDownloadReady(itemID int64, audioPath, videoPath string, bytes int64) error {
	return s.markReady(itemID, audioPath, videoPath, bytes)
}

// markReady records a finished download. Any clean audio was made from the
// previous one, so it is forgotten and, if there was one, made again.
func (s *Store) markReady(itemID int64, audioPath, videoPath string, bytes int64) error {
	_, err := s.db.Exec(`
		UPDATE downloads SET state = 'ready', local_path = ?, video_path = ?, bytes = ?,
			completed_at = unixepoch(), package_state = '', package_error = '',
			clean_track_path = '', clean_track_error = '',
			edges_edit = '', edges_error = '', edges_index = '',
			profanity_error = '', transcribe_error = '',
			needs_profanity = CASE WHEN clean_audio_path != '' THEN 1 ELSE needs_profanity END,
			clean_audio_path = ''
		WHERE item_id = ?`, audioPath, videoPath, bytes, itemID)
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

// SetCleanAudioPath updates the clean_audio_path for a download row. A new
// clean file has no track yet, so the old track and its error are cleared, and
// with them the edges, which were planned against the old track.
func (s *Store) SetCleanAudioPath(itemID int64, path string) error {
	_, err := s.db.Exec(`UPDATE downloads SET clean_audio_path = ?,
			clean_track_path = '', clean_track_error = '',
			edges_edit = '', edges_error = '', edges_index = ''
		WHERE item_id = ?`, path, itemID)
	if err != nil {
		return fmt.Errorf("set clean audio path: %w", err)
	}
	return nil
}

// DownloadFiles returns every file a download row names: media (audio and
// video tracks), cleaned audio and captions.
func (s *Store) DownloadFiles() ([]string, error) {
	rows, err := s.db.Query(`SELECT local_path FROM downloads WHERE local_path != ''
		UNION SELECT video_path FROM downloads WHERE video_path != ''
		UNION SELECT clean_audio_path FROM downloads WHERE clean_audio_path != ''
		UNION SELECT clean_track_path FROM downloads WHERE clean_track_path != ''
		UNION SELECT captions_path FROM downloads WHERE captions_path != ''`)
	if err != nil {
		return nil, fmt.Errorf("download files: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("download files: %w", err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// SetCaptionsPath updates the captions_path for a download row. Muting
// reads the captions, so its error is forgotten and it is tried afresh.
func (s *Store) SetCaptionsPath(itemID int64, path string) error {
	_, err := s.db.Exec(`UPDATE downloads SET captions_path = ?, profanity_error = '' WHERE item_id = ?`, path, itemID)
	if err != nil {
		return fmt.Errorf("set captions path: %w", err)
	}
	return nil
}

// FindDownload is GetDownload for callers to whom an item that was never
// downloaded is not an error: it returns the zero Download instead.
func (s *Store) FindDownload(itemID int64) (Download, error) {
	d, err := s.GetDownload(itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return Download{}, nil
	}
	return d, err
}

// GetDownload returns the download record for an item.
func (s *Store) GetDownload(itemID int64) (Download, error) {
	var d Download
	var needsProf, needsTrans, wanted int
	err := s.db.QueryRow(`
		SELECT item_id, state, local_path, clean_audio_path, captions_path,
		       bytes, error, started_at, completed_at, needs_profanity, needs_transcribe,
		       silence_ranges, silence_state, video_path, package_state, package_error,
		       clean_track_path, clean_track_error, edges_wanted, edges_edit, edges_error, edges_index,
		       profanity_error, transcribe_error
		FROM downloads WHERE item_id = ?`, itemID).Scan(
		&d.ItemID, &d.State, &d.LocalPath, &d.CleanAudioPath, &d.CaptionsPath,
		&d.Bytes, &d.Error, &d.StartedAt, &d.CompletedAt, &needsProf, &needsTrans,
		&d.SilenceRanges, &d.SilenceState, &d.VideoPath, &d.PackageState, &d.PackageError,
		&d.CleanTrackPath, &d.CleanTrackError, &wanted, &d.EdgesEdit, &d.EdgesError, &d.EdgesIndex,
		&d.ProfanityError, &d.TranscribeError,
	)
	if err != nil {
		return Download{}, fmt.Errorf("get download: %w", err)
	}
	d.EdgesWanted = wanted != 0
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
	return d.silenceRanges()
}

// silenceRanges decodes d's cached silence ranges.
func (d Download) silenceRanges() ([]Range, error) {
	if d.SilenceRanges == "" {
		return nil, nil
	}
	var rs []Range
	if err := json.Unmarshal([]byte(d.SilenceRanges), &rs); err != nil {
		return nil, fmt.Errorf("decode silence ranges for item %d: %w", d.ItemID, err)
	}
	return rs, nil
}

// PendingSilence returns up to limit item IDs whose download is ready on disk
// and whose silence ranges have not been computed.
func (s *Store) PendingSilence(limit int) ([]int64, error) {
	rows, err := s.db.Query(
		`SELECT item_id FROM downloads
		 WHERE state = 'ready' AND local_path != '' AND silence_state = ''
		 ORDER BY item_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending silence: %w", err)
	}
	return scanInt64IDs(rows, "pending silence")
}

// SetSilenceRanges caches silence ranges for an item and marks detection done.
// The cuts come from them, so any edges made are forgotten.
func (s *Store) SetSilenceRanges(itemID int64, ranges []Range) error {
	buf, err := json.Marshal(ranges)
	if err != nil {
		return fmt.Errorf("encode silence ranges: %w", err)
	}
	_, err = s.db.Exec(
		`UPDATE downloads SET silence_ranges = ?, silence_state = 'done',
			edges_edit = '', edges_error = '', edges_index = '' WHERE item_id = ?`,
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

// MarkProfanityError records why profanity muting failed. needs_profanity
// stays set, so the worker tries again; edges stop waiting for the clean
// rendition (PendingEdges). New captions, a new download or a success
// clear it.
func (s *Store) MarkProfanityError(itemID int64, msg string) error {
	if _, err := s.db.Exec(`UPDATE downloads SET profanity_error = ? WHERE item_id = ?`, msg, itemID); err != nil {
		return fmt.Errorf("mark profanity error: %w", err)
	}
	return nil
}

// MarkTranscribeError records why transcription failed. needs_transcribe
// stays set, so the worker tries again; edges stop waiting for the captions
// muting needs (PendingEdges). A new download or a success clear it.
func (s *Store) MarkTranscribeError(itemID int64, msg string) error {
	if _, err := s.db.Exec(`UPDATE downloads SET transcribe_error = ? WHERE item_id = ?`, msg, itemID); err != nil {
		return fmt.Errorf("mark transcribe error: %w", err)
	}
	return nil
}

// ClearNeedsProfanity clears the needs_profanity flag for a download, and
// the error of an earlier try.
func (s *Store) ClearNeedsProfanity(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET needs_profanity = 0, profanity_error = '' WHERE item_id = ?`, itemID)
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

// ClearNeedsTranscribe clears the needs_transcribe flag for a download, and
// the error of an earlier try.
func (s *Store) ClearNeedsTranscribe(itemID int64) error {
	_, err := s.db.Exec(`UPDATE downloads SET needs_transcribe = 0, transcribe_error = '' WHERE item_id = ?`, itemID)
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
			&d.VideoPath, &d.PackageState, &d.PackageError,
			&d.CleanTrackPath, &d.CleanTrackError,
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

// SetPlayback records playback progress for an item as of at, when it
// happened on the client that played it. An update older than the one stored
// is ignored, so a device syncing offline progress late cannot rewind a
// position set since; last writer wins by when it played, not when it
// arrived. A time in the future is taken as now, so a client with a fast
// clock cannot lock out every later update.
func (s *Store) SetPlayback(itemID int64, progressSec float64, completed bool, at time.Time) error {
	if now := time.Now(); at.After(now) {
		at = now
	}
	_, err := s.db.Exec(`
		INSERT INTO playback (item_id, progress_sec, completed, last_played_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(item_id) DO UPDATE SET
			progress_sec = excluded.progress_sec,
			completed = excluded.completed,
			last_played_at = excluded.last_played_at
		WHERE excluded.last_played_at >= playback.last_played_at`,
		itemID, progressSec, boolToInt(completed), at.Unix(),
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
	DownloadError     string // why the last download failed, when DownloadState is "error"
	HasCleanAudio     bool
	CleanPending      bool   // a clean file is waiting to be packaged as a track
	SourceKind        string // subscription.kind
	Packaged          bool   // the download is packaged for HLS
	HasVideo          bool   // the download has a video track apart from its audio
	HLSURL            string // the signed master playlist; set by handlers, not the store
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
			COALESCE(d.error, ''),
			CASE WHEN d.clean_audio_path != '' AND d.clean_audio_path IS NOT NULL THEN 1 ELSE 0 END,
			COALESCE(d.package_state = 'done', 0),
			COALESCE(d.video_path != '', 0),
			COALESCE(d.clean_audio_path != '' AND d.clean_track_path = '', 0)
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
		&ip.DownloadError,
		&hasClean,
		&ip.Packaged,
		&ip.HasVideo,
		&ip.CleanPending,
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

// CreatePlaylist inserts a new playlist and returns its ID. Its order is
// newest first unless p says otherwise.
func (s *Store) CreatePlaylist(p Playlist) (int64, error) {
	if p.Order == "" {
		p.Order = OrderNewest
	}
	res, err := s.db.Exec(`INSERT INTO playlists (name, query, sort_order, play_order, shuffle_seed, listen_only) VALUES (?, ?, ?, ?, ?, ?)`,
		p.Name, p.Query, p.SortOrder, p.Order, p.ShuffleSeed, p.ListenOnly)
	if err != nil {
		return 0, fmt.Errorf("create playlist: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("create playlist last insert id: %w", err)
	}
	return id, nil
}

const playlistColumns = `id, name, query, sort_order, created_at, play_order, shuffle_seed, listen_only`

func scanPlaylist(row interface{ Scan(...any) error }) (Playlist, error) {
	var p Playlist
	err := row.Scan(&p.ID, &p.Name, &p.Query, &p.SortOrder, &p.CreatedAt, &p.Order, &p.ShuffleSeed, &p.ListenOnly)
	return p, err
}

// ListPlaylists returns all playlists ordered by sort_order, name.
func (s *Store) ListPlaylists() ([]Playlist, error) {
	rows, err := s.db.Query(`SELECT ` + playlistColumns + ` FROM playlists ORDER BY sort_order, name, id`)
	if err != nil {
		return nil, fmt.Errorf("list playlists: %w", err)
	}
	defer rows.Close()
	var playlists []Playlist
	for rows.Next() {
		p, err := scanPlaylist(rows)
		if err != nil {
			return nil, fmt.Errorf("list playlists scan: %w", err)
		}
		playlists = append(playlists, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list playlists rows: %w", err)
	}
	return playlists, nil
}

// UpdatePlaylist updates an existing playlist. It returns sql.ErrNoRows
// when there is none with p's ID.
func (s *Store) UpdatePlaylist(p Playlist) error {
	if p.Order == "" {
		p.Order = OrderNewest
	}
	res, err := s.db.Exec(`
		UPDATE playlists SET name = ?, query = ?, sort_order = ?, play_order = ?, shuffle_seed = ?, listen_only = ?
		WHERE id = ?`,
		p.Name, p.Query, p.SortOrder, p.Order, p.ShuffleSeed, p.ListenOnly, p.ID,
	)
	if err != nil {
		return fmt.Errorf("update playlist: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update playlist rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("update playlist %d: %w", p.ID, sql.ErrNoRows)
	}
	return nil
}

// GetPlaylist returns a single playlist by ID.
func (s *Store) GetPlaylist(id int64) (Playlist, error) {
	p, err := scanPlaylist(s.db.QueryRow(`SELECT `+playlistColumns+` FROM playlists WHERE id = ?`, id))
	if err != nil {
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
	TotalItems     int
	Downloaded     int // items whose download is ready to play
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
	if err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(is_starred), 0),
		       (SELECT COUNT(*) FROM downloads WHERE state = 'ready')
		FROM items`).Scan(&sc.TotalItems, &sc.StarredCount, &sc.Downloaded); err != nil {
		return sc, fmt.Errorf("sidebar item counts: %w", err)
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
	// History keeps the items played, most recently played first, whatever
	// Order says.
	History  bool
	HideRead bool
	Query    query.Expr    // optional cog-grammar filter; compiled to SQL.
	Schema   *query.Schema // required when Query != nil.
	// Unfinished keeps audio and video not yet played to the end: what a
	// play queue holds.
	Unfinished bool
	Order      PlayOrder // "" is newest first
	Seed       int64     // the shuffle, when Order is OrderShuffled
	// After continues a list from the item it last returned (keyset
	// pagination): only items after it in this order are listed.
	After *Cursor

	joinPlayback bool // join playback as pb whatever else needs it
}

// Cursor is an item's place in a list's order: its sort key and, to break
// ties, its id.
type Cursor struct {
	Key int64
	ID  int64
}

// unfinishedSQL is ViewSpec.Unfinished's condition; it needs playbackJoin.
const unfinishedSQL = "i.type != 'text' AND COALESCE(pb.completed, 0) = 0"

const playbackJoin = " LEFT JOIN playback pb ON pb.item_id = i.id"

// leftSQL is the seconds left to play of an item; it needs playbackJoin.
const leftSQL = "MAX(i.duration_sec - CAST(COALESCE(pb.progress_sec, 0) AS INTEGER), 0)"

// from returns the joins and conditions after `FROM items i JOIN
// subscriptions s`, shared by the item list and the counts.
func (spec ViewSpec) from() (joins string, where []string, args []any, err error) {
	var b strings.Builder
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
		b.WriteString(" JOIN item_tags it ON it.item_id = i.id JOIN tags t ON t.id = it.tag_id")
		where = append(where, "t.name = ?")
		args = append(args, spec.TagName)
	}
	// spec.All: no extra WHERE

	if spec.HideRead {
		where = append(where, "i.is_read = 0")
	}
	switch {
	case spec.History:
		b.WriteString(" JOIN playback pb ON pb.item_id = i.id")
	case spec.Unfinished || spec.Order == OrderShortest || spec.joinPlayback:
		b.WriteString(playbackJoin)
	}
	if spec.Unfinished {
		where = append(where, unfinishedSQL)
	}

	if spec.Query != nil {
		if spec.Schema == nil {
			return "", nil, nil, fmt.Errorf("Query set but Schema is nil")
		}
		w, qArgs, joinNames, err := query.Compile(spec.Query, *spec.Schema)
		if err != nil {
			return "", nil, nil, fmt.Errorf("compile: %w", err)
		}
		for _, name := range joinNames {
			b.WriteByte(' ')
			b.WriteString(spec.Schema.Joins[name])
		}
		if w != "" {
			where = append(where, w)
			args = append(args, qArgs...)
		}
	}
	return b.String(), where, args, nil
}

// sortKey is the integer the list is ordered by, then by id in the same
// direction; one key and the id are what a Cursor holds.
func (spec ViewSpec) sortKey() (expr string, args []any, desc bool) {
	switch {
	case spec.History:
		return "pb.last_played_at", nil, true
	case spec.Order == OrderOldest:
		return "i.published_at", nil, false
	case spec.Order == OrderShortest:
		return leftSQL, nil, false
	case spec.Order == OrderShuffled:
		// A hash of the id and the seed, so the order holds still until the
		// seed changes: x spreads the id and the seed, and squaring it mod a
		// prime breaks up the arithmetic progression x alone leaves. x stays
		// below 2^31 and its square below 2^63 for seeds below 10^9.
		x := "((i.id * 48271 + ? * 69621) % 1000000007)"
		seed := spec.Seed % 1000000007
		return "(" + x + " * " + x + " % 1000000007)", []any{seed, seed}, false
	default:
		return "i.published_at", nil, true
	}
}

// ListItemsForView returns items (with subscription title) according to the ViewSpec.
func (s *Store) ListItemsForView(spec ViewSpec, limit int) ([]ItemWithSubscription, error) {
	items, _, err := s.listItems(spec, limit)
	return items, err
}

// listItems is ListItemsForView, with each item's place in the order.
func (s *Store) listItems(spec ViewSpec, limit int) ([]ItemWithSubscription, []Cursor, error) {
	joins, where, args, err := spec.from()
	if err != nil {
		return nil, nil, fmt.Errorf("list items for view: %w", err)
	}
	key, keyArgs, desc := spec.sortKey()
	q := `
		SELECT i.id, i.subscription_id, i.guid, i.type, i.title, i.author, i.url,
		       i.content_html, i.media_url, i.media_mime, i.duration_sec, i.thumbnail_url,
		       i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		       COALESCE(s.title, s.url) AS subscription_title, ` + key + `
		FROM items i
		JOIN subscriptions s ON i.subscription_id = s.id` + joins
	args = append(slices.Clone(keyArgs), args...)
	dir, cmp := "ASC", ">"
	if desc {
		dir, cmp = "DESC", "<"
	}
	if spec.After != nil {
		where = append(where, "("+key+", i.id) "+cmp+" (?, ?)")
		args = append(append(args, keyArgs...), spec.After.Key, spec.After.ID)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY " + key + " " + dir + ", i.id " + dir + " LIMIT ?"
	args = append(append(args, keyArgs...), limit)

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("list items for view: %w", err)
	}
	defer rows.Close()

	var items []ItemWithSubscription
	var keys []Cursor
	for rows.Next() {
		var iws ItemWithSubscription
		var isRead, isStarred int
		var k int64
		if err := rows.Scan(
			&iws.ID, &iws.SubscriptionID, &iws.GUID, &iws.Type, &iws.Title, &iws.Author,
			&iws.URL, &iws.ContentHTML, &iws.MediaURL, &iws.MediaMIME, &iws.DurationSec,
			&iws.ThumbnailURL, &iws.PublishedAt, &iws.FetchedAt, &isRead, &isStarred,
			&iws.ContentHash, &iws.SubscriptionTitle, &k,
		); err != nil {
			return nil, nil, fmt.Errorf("list items for view scan: %w", err)
		}
		iws.IsRead = isRead != 0
		iws.IsStarred = isStarred != 0
		items = append(items, iws)
		keys = append(keys, Cursor{Key: k, ID: iws.ID})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("list items for view rows: %w", err)
	}
	return items, keys, nil
}

// MarkAllRead marks all items as read.
func (s *Store) MarkAllRead() error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE is_read = 0`)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// MarkAllReadBySubscription marks all items in a subscription as read.
func (s *Store) MarkAllReadBySubscription(subID int64) error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE subscription_id = ? AND is_read = 0`, subID)
	if err != nil {
		return fmt.Errorf("mark all read by subscription: %w", err)
	}
	return nil
}

// MarkAllReadByFolder marks all items in subscriptions belonging to a folder as read.
func (s *Store) MarkAllReadByFolder(folderID int64) error {
	_, err := s.db.Exec(`
		UPDATE items SET is_read = 1, fetched_at = unixepoch()
		WHERE is_read = 0 AND subscription_id IN (SELECT id FROM subscriptions WHERE folder_id = ?)`, folderID)
	if err != nil {
		return fmt.Errorf("mark all read by folder: %w", err)
	}
	return nil
}

// MarkItemRead force-sets a single item as read (non-toggle).
func (s *Store) MarkItemRead(id int64) error {
	_, err := s.db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE id = ? AND is_read = 0`, id)
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
		 FROM items WHERE is_read = 0 ORDER BY published_at DESC, id DESC LIMIT ?`, limit)
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
		 FROM items ORDER BY published_at DESC, id DESC LIMIT ?`, limit)
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

// TagCount is a tag and how many items carry it.
type TagCount struct {
	Name  string
	Count int64
}

// ListTagCounts returns every tag with its item count, ordered by name.
func (s *Store) ListTagCounts() ([]TagCount, error) {
	rows, err := s.db.Query(`
		SELECT t.name, COUNT(it.item_id)
		FROM tags t LEFT JOIN item_tags it ON it.tag_id = t.id
		GROUP BY t.id ORDER BY t.name`)
	if err != nil {
		return nil, fmt.Errorf("list tag counts: %w", err)
	}
	defer rows.Close()
	var out []TagCount
	for rows.Next() {
		var tc TagCount
		if err := rows.Scan(&tc.Name, &tc.Count); err != nil {
			return nil, fmt.Errorf("scan tag count: %w", err)
		}
		out = append(out, tc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tag counts rows: %w", err)
	}
	return out, nil
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

// TagsByItems returns tag names per item ID in one query.
// Items with no tags are absent from the map.
func (s *Store) TagsByItems(ids []int64) (map[int64][]string, error) {
	if len(ids) == 0 {
		return map[int64][]string{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT it.item_id, t.name FROM item_tags it
		JOIN tags t ON t.id = it.tag_id
		WHERE it.item_id IN (`+placeholders+`)
		ORDER BY it.item_id, t.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("tags by items: %w", err)
	}
	defer rows.Close()
	out := make(map[int64][]string)
	for rows.Next() {
		var itemID int64
		var name string
		if err := rows.Scan(&itemID, &name); err != nil {
			return nil, fmt.Errorf("tags by items scan: %w", err)
		}
		out[itemID] = append(out[itemID], name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("tags by items rows: %w", err)
	}
	return out, nil
}

// idArgs is ids as a placeholder list and its arguments, for IN (…).
func idArgs(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// PlaybackByItems returns playback progress per item ID in one query. Items
// never played are absent from the map.
func (s *Store) PlaybackByItems(ids []int64) (map[int64]Playback, error) {
	out := make(map[int64]Playback)
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idArgs(ids)
	rows, err := s.db.Query(`
		SELECT item_id, progress_sec, completed, last_played_at
		FROM playback WHERE item_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("playback by items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var pb Playback
		if err := rows.Scan(&pb.ItemID, &pb.ProgressSec, &pb.Completed, &pb.LastPlayedAt); err != nil {
			return nil, fmt.Errorf("playback by items scan: %w", err)
		}
		out[pb.ItemID] = pb
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("playback by items rows: %w", err)
	}
	return out, nil
}

// DownloadsByItems returns the download row per item ID in one query, with
// what a client is shown of it: state, error, size and captions. Items with
// no download are absent from the map.
func (s *Store) DownloadsByItems(ids []int64) (map[int64]Download, error) {
	out := make(map[int64]Download)
	if len(ids) == 0 {
		return out, nil
	}
	in, args := idArgs(ids)
	rows, err := s.db.Query(`
		SELECT item_id, state, error, bytes, captions_path
		FROM downloads WHERE item_id IN (`+in+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("downloads by items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d Download
		if err := rows.Scan(&d.ItemID, &d.State, &d.Error, &d.Bytes, &d.CaptionsPath); err != nil {
			return nil, fmt.Errorf("downloads by items scan: %w", err)
		}
		out[d.ItemID] = d
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("downloads by items rows: %w", err)
	}
	return out, nil
}

// DownloadStateByItems returns the download state per item ID in one query.
// Items with no download row map to "".
func (s *Store) DownloadStateByItems(ids []int64) (map[int64]string, error) {
	if len(ids) == 0 {
		return map[int64]string{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT item_id, state FROM downloads
		WHERE item_id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("download state by items: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]string)
	for rows.Next() {
		var itemID int64
		var state string
		if err := rows.Scan(&itemID, &state); err != nil {
			return nil, fmt.Errorf("download state by items scan: %w", err)
		}
		out[itemID] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("download state by items rows: %w", err)
	}
	return out, nil
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

	settingSkipSilenceMinSec = FloatSetting("skip_silence_min_sec", 1.0)
)

// The bounds of the player preferences a client may set.
const (
	MinSpeed   = 0.25
	MaxSpeed   = 4.0
	MinSkipSec = 1
	MaxSkipSec = 120

	MinSkipSilenceSec = 0.5
	MaxSkipSilenceSec = 10.0
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

	// SkipSilenceMinSec is the shortest silence skip silence cuts.
	SkipSilenceMinSec float64 `json:"skipSilenceMinSec"`
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
	if p.SkipSilenceMinSec, err = settingSkipSilenceMinSec.Get(s.db); err != nil {
		return p, fmt.Errorf("load skip_silence_min_sec: %w", err)
	}
	return p, nil
}

// SavePlayerPrefs persists the per-user player preferences.
// When the shortest silence skipped changes, so do the cuts: every item's
// edges are forgotten (and remade for the items players want).
func (s *Store) SavePlayerPrefs(p PlayerPrefs) error {
	oldMin, err := settingSkipSilenceMinSec.Get(s.db)
	if err != nil {
		return fmt.Errorf("load skip_silence_min_sec: %w", err)
	}
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
	if err := settingSkipSilenceMinSec.Set(s.db, p.SkipSilenceMinSec); err != nil {
		return fmt.Errorf("save skip_silence_min_sec: %w", err)
	}
	if p.SkipSilenceMinSec != oldMin {
		return s.ResetEdges()
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
		 ORDER BY pb.last_played_at DESC, i.id DESC
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

// --- Per-subscription settings store helpers ---

// SubscriptionChanges are the settings a user changes on a subscription; a
// nil field is left as it is.
type SubscriptionChanges struct {
	Folder *sql.NullInt64 // NULL takes it out of its folder
	// Title is the user's name for it, which the poller then leaves alone.
	// "" goes back to the feed's own title, from a full fetch the next time
	// it is polled.
	Title           *string
	PollIntervalSec *int64
	Paused          *bool
}

// UpdateSubscription applies c to a subscription in one statement, and
// returns sql.ErrNoRows if there is none.
func (s *Store) UpdateSubscription(id int64, c SubscriptionChanges) error {
	var set []string
	var args []any
	if c.Folder != nil {
		set = append(set, "folder_id = ?")
		args = append(args, *c.Folder)
	}
	if c.Title != nil {
		if *c.Title == "" {
			set = append(set, "title_locked = 0", "etag = ''", "last_modified = ''", "next_check_at = 1")
		} else {
			set = append(set, "title = ?", "title_locked = 1")
			args = append(args, *c.Title)
		}
	}
	if c.PollIntervalSec != nil {
		set = append(set, "poll_interval_sec = ?")
		args = append(args, *c.PollIntervalSec)
	}
	if c.Paused != nil {
		set = append(set, "paused = ?")
		args = append(args, boolToInt(*c.Paused))
	}
	if len(set) == 0 {
		_, err := s.GetSubscription(id) // still reports a missing subscription
		return err
	}
	res, err := s.db.Exec(`UPDATE subscriptions SET `+strings.Join(set, ", ")+` WHERE id = ?`, append(args, id)...)
	return updatedOne(res, err, "update subscription %d", id)
}

// --- Bulk subscription changes ---

// idsJSON encodes ids for json_each, which takes any number of them as one
// bind argument.
func idsJSON(ids []int64) (string, error) {
	b, err := json.Marshal(ids)
	if err != nil {
		return "", fmt.Errorf("encode ids: %w", err)
	}
	return string(b), nil
}

// MoveSubscriptions puts the given subscriptions in a folder (none when
// folderID is invalid), returning how many there were. A folder deleted in
// the meantime moves them to none.
func (s *Store) MoveSubscriptions(ids []int64, folderID sql.NullInt64) (int64, error) {
	j, err := idsJSON(ids)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`
		UPDATE subscriptions SET folder_id = (SELECT id FROM folders WHERE id = ?)
		WHERE id IN (SELECT value FROM json_each(?))`, folderID, j)
	if err != nil {
		return 0, fmt.Errorf("move subscriptions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("move subscriptions rows affected: %w", err)
	}
	return n, nil
}

// DeleteSubscriptions removes the given subscriptions, returning how many
// there were.
func (s *Store) DeleteSubscriptions(ids []int64) (int64, error) {
	j, err := idsJSON(ids)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`DELETE FROM subscriptions WHERE id IN (SELECT value FROM json_each(?))`, j)
	if err != nil {
		return 0, fmt.Errorf("delete subscriptions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("delete subscriptions rows affected: %w", err)
	}
	return n, nil
}

// EnsureFolder returns the ID of the folder with the given name, creating it
// if there is none.
func (s *Store) EnsureFolder(name string) (int64, error) {
	var id int64
	err := s.db.QueryRow(`
		INSERT INTO folders (name) VALUES (?)
		ON CONFLICT (name) DO UPDATE SET name = excluded.name
		RETURNING id`, name).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("ensure folder %q: %w", name, err)
	}
	return id, nil
}

// SubscriptionsWithMedia returns the IDs of subscriptions that have an audio
// or video item: the podcasts, among RSS feeds.
func (s *Store) SubscriptionsWithMedia() (map[int64]bool, error) {
	rows, err := s.db.Query(`
		SELECT id FROM subscriptions s
		WHERE EXISTS (SELECT 1 FROM items i WHERE i.subscription_id = s.id AND i.type IN ('audio', 'video'))`)
	if err != nil {
		return nil, fmt.Errorf("subscriptions with media: %w", err)
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("subscriptions with media scan: %w", err)
		}
		out[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("subscriptions with media rows: %w", err)
	}
	return out, nil
}
