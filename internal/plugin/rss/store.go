package rss

import (
	"database/sql"
	"fmt"
	"strings"
)

// Folder is a logical grouping of feeds.
type Folder struct {
	ID        int64
	Name      string
	SortOrder int
	CreatedAt int64
}

// Feed is an RSS/Atom subscription source.
type Feed struct {
	ID              int64
	FolderID        sql.NullInt64
	URL             string
	SiteURL         string
	Title           string
	FaviconURL      string
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
	AutoReadPattern string
}

// Item is a normalized feed entry with read/starred state.
type Item struct {
	ID          int64
	FeedID      int64
	GUID        string
	Title       string
	Author      string
	URL         string
	ContentHTML string
	PublishedAt int64
	FetchedAt   int64
	IsRead      bool
	IsStarred   bool
	ContentHash string
}

// ItemWithFeed extends Item with the feed title for display.
type ItemWithFeed struct {
	Item
	FeedTitle string
}

// --- Folder CRUD ---

func ListFolders(db *sql.DB) ([]Folder, error) {
	rows, err := db.Query(`SELECT id, name, sort_order, created_at FROM folders ORDER BY sort_order, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var folders []Folder
	for rows.Next() {
		var f Folder
		if err := rows.Scan(&f.ID, &f.Name, &f.SortOrder, &f.CreatedAt); err != nil {
			return nil, err
		}
		folders = append(folders, f)
	}
	return folders, rows.Err()
}

func CreateFolder(db *sql.DB, name string) (*Folder, error) {
	res, err := db.Exec(`INSERT INTO folders (name) VALUES (?)`, name)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Folder{ID: id, Name: name}, nil
}

func DeleteFolder(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	return err
}

func GetFolder(db *sql.DB, id int64) (*Folder, error) {
	var f Folder
	err := db.QueryRow(`SELECT id, name, sort_order, created_at FROM folders WHERE id = ?`, id).
		Scan(&f.ID, &f.Name, &f.SortOrder, &f.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func RenameFolder(db *sql.DB, id int64, name string) error {
	_, err := db.Exec(`UPDATE folders SET name = ? WHERE id = ?`, name, id)
	return err
}

// --- Feed CRUD ---

func ListFeeds(db *sql.DB) ([]Feed, error) {
	rows, err := db.Query(`SELECT id, folder_id, url, site_url, title, favicon_url,
		etag, last_modified, poll_interval_sec, next_check_at, last_checked_at,
		last_success_at, error_count, last_error, paused, created_at, auto_read_pattern
		FROM feeds ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

func GetFeed(db *sql.DB, id int64) (*Feed, error) {
	var f Feed
	err := db.QueryRow(`SELECT id, folder_id, url, site_url, title, favicon_url,
		etag, last_modified, poll_interval_sec, next_check_at, last_checked_at,
		last_success_at, error_count, last_error, paused, created_at, auto_read_pattern
		FROM feeds WHERE id = ?`, id).Scan(
		&f.ID, &f.FolderID, &f.URL, &f.SiteURL, &f.Title, &f.FaviconURL,
		&f.ETag, &f.LastModified, &f.PollIntervalSec, &f.NextCheckAt,
		&f.LastCheckedAt, &f.LastSuccessAt, &f.ErrorCount, &f.LastError,
		&f.Paused, &f.CreatedAt, &f.AutoReadPattern)
	if err != nil {
		return nil, err
	}
	return &f, nil
}

func CreateFeed(db *sql.DB, url string, folderID sql.NullInt64) (*Feed, error) {
	res, err := db.Exec(`INSERT INTO feeds (url, folder_id) VALUES (?, ?)`, url, folderID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetFeed(db, id)
}

func DeleteFeed(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM feeds WHERE id = ?`, id)
	return err
}

func MoveFeed(db *sql.DB, id int64, folderID sql.NullInt64) error {
	_, err := db.Exec(`UPDATE feeds SET folder_id = ? WHERE id = ?`, folderID, id)
	return err
}

func RenameFeed(db *sql.DB, id int64, title string) error {
	_, err := db.Exec(`UPDATE feeds SET title = ? WHERE id = ?`, title, id)
	return err
}

func SetFeedAutoReadPattern(db *sql.DB, id int64, pattern string) error {
	_, err := db.Exec(`UPDATE feeds SET auto_read_pattern = ? WHERE id = ?`, pattern, id)
	return err
}

// FeedsDue returns feeds that need to be checked.
func FeedsDue(db *sql.DB, now int64) ([]Feed, error) {
	rows, err := db.Query(`SELECT id, folder_id, url, site_url, title, favicon_url,
		etag, last_modified, poll_interval_sec, next_check_at, last_checked_at,
		last_success_at, error_count, last_error, paused, created_at, auto_read_pattern
		FROM feeds WHERE next_check_at <= ? AND paused = 0`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFeeds(rows)
}

func scanFeeds(rows *sql.Rows) ([]Feed, error) {
	var feeds []Feed
	for rows.Next() {
		var f Feed
		if err := rows.Scan(
			&f.ID, &f.FolderID, &f.URL, &f.SiteURL, &f.Title, &f.FaviconURL,
			&f.ETag, &f.LastModified, &f.PollIntervalSec, &f.NextCheckAt,
			&f.LastCheckedAt, &f.LastSuccessAt, &f.ErrorCount, &f.LastError,
			&f.Paused, &f.CreatedAt, &f.AutoReadPattern); err != nil {
			return nil, err
		}
		feeds = append(feeds, f)
	}
	return feeds, rows.Err()
}

// --- Item CRUD ---

// UnreadItems returns unread items across all feeds, newest first.
func UnreadItems(db *sql.DB, limit int) ([]ItemWithFeed, error) {
	return queryItems(db, `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id
		WHERE i.is_read = 0
		ORDER BY i.published_at DESC LIMIT ?`, limit)
}

// StarredItems returns starred items, newest first.
func StarredItems(db *sql.DB, limit int) ([]ItemWithFeed, error) {
	return queryItems(db, `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id
		WHERE i.is_starred = 1
		ORDER BY i.published_at DESC LIMIT ?`, limit)
}

// FeedItems returns items for a specific feed, newest first.
func FeedItems(db *sql.DB, feedID int64, limit int) ([]ItemWithFeed, error) {
	return queryItems(db, `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id
		WHERE i.feed_id = ?
		ORDER BY i.published_at DESC LIMIT ?`, feedID, limit)
}

// FolderItems returns unread items for all feeds in a folder, newest first.
func FolderItems(db *sql.DB, folderID int64, limit int) ([]ItemWithFeed, error) {
	return queryItems(db, `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id
		WHERE f.folder_id = ?
		ORDER BY i.published_at DESC LIMIT ?`, folderID, limit)
}

// AllItems returns all items, newest first (for NC News API).
func AllItems(db *sql.DB, limit, offset int, typ int, id int64, getRead bool) ([]ItemWithFeed, error) {
	query := `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id`

	var conditions []string
	var args []any

	if !getRead {
		conditions = append(conditions, "i.is_read = 0")
	}

	switch typ {
	case 0: // feed
		if id > 0 {
			conditions = append(conditions, "i.feed_id = ?")
			args = append(args, id)
		}
	case 1: // folder
		if id > 0 {
			conditions = append(conditions, "f.folder_id = ?")
			args = append(args, id)
		}
	case 2: // starred
		conditions = append(conditions, "i.is_starred = 1")
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	query += " ORDER BY i.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	return queryItems(db, query, args...)
}

// UpdatedItems returns items modified since the given timestamp.
func UpdatedItems(db *sql.DB, lastModified int64, typ int, id int64) ([]ItemWithFeed, error) {
	query := `SELECT i.id, i.feed_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		f.title
		FROM items i JOIN feeds f ON i.feed_id = f.id
		WHERE i.fetched_at >= ?`
	args := []any{lastModified}

	switch typ {
	case 0: // feed
		if id > 0 {
			query += " AND i.feed_id = ?"
			args = append(args, id)
		}
	case 1: // folder
		if id > 0 {
			query += " AND f.folder_id = ?"
			args = append(args, id)
		}
	}

	query += " ORDER BY i.id DESC"
	return queryItems(db, query, args...)
}

func queryItems(db *sql.DB, query string, args ...any) ([]ItemWithFeed, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []ItemWithFeed
	for rows.Next() {
		var it ItemWithFeed
		if err := rows.Scan(
			&it.ID, &it.FeedID, &it.GUID, &it.Title, &it.Author, &it.URL,
			&it.ContentHTML, &it.PublishedAt, &it.FetchedAt, &it.IsRead,
			&it.IsStarred, &it.ContentHash, &it.FeedTitle); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

func ToggleItemRead(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE items SET is_read = 1 - is_read, fetched_at = unixepoch() WHERE id = ?`, id)
	return err
}

func ToggleItemStar(db *sql.DB, id int64) error {
	_, err := db.Exec(`UPDATE items SET is_starred = 1 - is_starred, fetched_at = unixepoch() WHERE id = ?`, id)
	return err
}

func SetItemRead(db *sql.DB, id int64, read bool) error {
	_, err := db.Exec(`UPDATE items SET is_read = ?, fetched_at = unixepoch() WHERE id = ?`, boolInt(read), id)
	return err
}

func SetItemStarred(db *sql.DB, id int64, starred bool) error {
	_, err := db.Exec(`UPDATE items SET is_starred = ?, fetched_at = unixepoch() WHERE id = ?`, boolInt(starred), id)
	return err
}

func MarkFeedRead(db *sql.DB, feedID int64) error {
	_, err := db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE feed_id = ? AND is_read = 0`, feedID)
	return err
}

func MarkAllRead(db *sql.DB) error {
	_, err := db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE is_read = 0`)
	return err
}

func MarkItemsRead(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_read", 1, ids)
}

func MarkItemsUnread(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_read", 0, ids)
}

func StarItems(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_starred", 1, ids)
}

func UnstarItems(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_starred", 0, ids)
}

func setItemsBool(db *sql.DB, column string, val int, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, val)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(`UPDATE items SET %s = ?, fetched_at = unixepoch() WHERE id IN (%s)`, column, strings.Join(placeholders, ","))
	_, err := db.Exec(query, args...)
	return err
}

// UnreadCount returns the total number of unread items.
func UnreadCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_read = 0`).Scan(&n)
	return n, err
}

// FeedUnreadCounts returns unread counts per feed.
func FeedUnreadCounts(db *sql.DB) (map[int64]int, error) {
	rows, err := db.Query(`SELECT feed_id, COUNT(*) FROM items WHERE is_read = 0 GROUP BY feed_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[int64]int)
	for rows.Next() {
		var feedID int64
		var count int
		if err := rows.Scan(&feedID, &count); err != nil {
			return nil, err
		}
		counts[feedID] = count
	}
	return counts, rows.Err()
}

// StarredCount returns the total number of starred items.
func StarredCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_starred = 1`).Scan(&n)
	return n, err
}

// GetItem returns a single item by ID.
func GetItem(db *sql.DB, id int64) (*Item, error) {
	var it Item
	err := db.QueryRow(`SELECT id, feed_id, guid, title, author, url, content_html,
		published_at, fetched_at, is_read, is_starred, content_hash
		FROM items WHERE id = ?`, id).Scan(
		&it.ID, &it.FeedID, &it.GUID, &it.Title, &it.Author, &it.URL,
		&it.ContentHTML, &it.PublishedAt, &it.FetchedAt, &it.IsRead,
		&it.IsStarred, &it.ContentHash)
	if err != nil {
		return nil, err
	}
	return &it, nil
}

// MarkFeedReadBefore marks all items for a feed as read where id <= newestItemID.
func MarkFeedReadBefore(db *sql.DB, feedID, newestItemID int64) error {
	_, err := db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch() WHERE feed_id = ? AND id <= ? AND is_read = 0`,
		feedID, newestItemID)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
