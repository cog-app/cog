package feed

import (
	"database/sql"
	"fmt"
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
