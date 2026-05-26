// Package ncapi implements the NextCloud News API v1-2 surface for the feed plugin.
// It exposes the same JSON wire shape (feeds/folders/items) as the rss plugin's
// newsapi.go but operates on the feed plugin's unified schema:
//   - "feeds"   → subscriptions WHERE kind='rss'
//   - "items"   → items WHERE type='text'
//   - "folders" → folders (unchanged)
//
// Import-cycle resolution: this package defines a Backend interface so that
// feed.Plugin can satisfy it without ncapi importing the feed package.
// feed.go imports ncapi (one direction only).
package ncapi

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
)

// Backend is the minimal interface that the feed plugin must satisfy.
// *feed.Plugin satisfies it; ncapi never imports the feed package directly.
type Backend interface {
	// DB returns the per-user plugin database (migrations already applied).
	DB(username string) (*sql.DB, error)
	// TriggerFetch schedules an immediate background fetch for a subscription.
	// Implementations may treat this as a no-op.
	TriggerFetch(username string, subID int64)
}

// NewsAPI implements the NextCloud News API v1-2 endpoints for the feed plugin.
type NewsAPI struct {
	backend Backend
}

// New creates a new NC News API handler backed by the provided Backend.
func New(b Backend) *NewsAPI {
	return &NewsAPI{backend: b}
}

func (a *NewsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		plugin.WriteJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	db, err := a.backend.DB(u.Username)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Strip the prefix to get the API path.
	path := r.URL.Path
	for _, prefix := range []string{"/index.php/apps/news/api/v1-2", "/index.php/apps/news/api"} {
		if after, ok := strings.CutPrefix(path, prefix); ok {
			path = after
			break
		}
	}
	if path == "" {
		path = "/"
	}

	switch {
	case r.Method == "GET" && (path == "/" || path == "/version"):
		a.version(w)
	// Folders
	case r.Method == "GET" && path == "/folders":
		a.listFolders(w, db)
	case r.Method == "POST" && path == "/folders":
		a.createFolder(w, r, db)
	case r.Method == "PUT" && strings.HasPrefix(path, "/folders/"):
		id := plugin.PathID(path, "/folders/")
		a.renameFolder(w, r, db, id)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/folders/"):
		id := plugin.PathID(path, "/folders/")
		a.deleteFolder(w, db, id)
	// Feeds (subscriptions WHERE kind='rss')
	case r.Method == "GET" && path == "/feeds":
		a.listFeeds(w, db)
	case r.Method == "POST" && path == "/feeds":
		a.createFeed(w, r, db, u.Username)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/feeds/"):
		id := plugin.PathID(path, "/feeds/")
		a.deleteFeed(w, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/move") && strings.HasPrefix(path, "/feeds/"):
		id := plugin.PathIDSuffix(path, "/feeds/", "/move")
		a.moveFeed(w, r, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/rename") && strings.HasPrefix(path, "/feeds/"):
		id := plugin.PathIDSuffix(path, "/feeds/", "/rename")
		a.renameFeed(w, r, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/read") && strings.HasPrefix(path, "/feeds/"):
		id := plugin.PathIDSuffix(path, "/feeds/", "/read")
		a.markFeedRead(w, r, db, id)
	// Items (WHERE type='text')
	case r.Method == "GET" && path == "/items":
		a.getItems(w, r, db)
	case r.Method == "GET" && path == "/items/updated":
		a.getUpdatedItems(w, r, db)
	case r.Method == "PUT" && strings.HasSuffix(path, "/read") && strings.HasPrefix(path, "/items/"):
		id := plugin.PathIDSuffix(path, "/items/", "/read")
		a.setItemRead(w, db, id, true)
	case r.Method == "PUT" && strings.HasSuffix(path, "/unread") && strings.HasPrefix(path, "/items/"):
		id := plugin.PathIDSuffix(path, "/items/", "/unread")
		a.setItemRead(w, db, id, false)
	case r.Method == "PUT" && strings.HasSuffix(path, "/star") && strings.HasPrefix(path, "/items/"):
		id := plugin.PathIDSuffix(path, "/items/", "/star")
		a.setItemStarred(w, db, id, true)
	case r.Method == "PUT" && strings.HasSuffix(path, "/unstar") && strings.HasPrefix(path, "/items/"):
		id := plugin.PathIDSuffix(path, "/items/", "/unstar")
		a.setItemStarred(w, db, id, false)
	// Bulk operations
	case r.Method == "PUT" && path == "/items/read/multiple":
		a.bulkRead(w, r, db, true)
	case r.Method == "PUT" && path == "/items/unread/multiple":
		a.bulkRead(w, r, db, false)
	case r.Method == "PUT" && path == "/items/star/multiple":
		a.bulkStar(w, r, db, true)
	case r.Method == "PUT" && path == "/items/unstar/multiple":
		a.bulkStar(w, r, db, false)
	default:
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *NewsAPI) version(w http.ResponseWriter) {
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"apiLevels": []string{"v1-2"},
	})
}

// --- Folders ---

func (a *NewsAPI) listFolders(w http.ResponseWriter, db *sql.DB) {
	folders, err := listFolders(db)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"folders": foldersToAPI(folders)})
}

func (a *NewsAPI) createFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	folder, err := createFolder(db, body.Name)
	if err != nil {
		plugin.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"folders": []any{folderToAPI(folder)}})
}

func (a *NewsAPI) renameFolder(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if err := renameFolder(db, id, body.Name); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) deleteFolder(w http.ResponseWriter, db *sql.DB, id int64) {
	if err := deleteFolder(db, id); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Feeds (subscriptions WHERE kind='rss') ---

func (a *NewsAPI) listFeeds(w http.ResponseWriter, db *sql.DB) {
	subs, err := listRSSSubscriptions(db)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	starredCount, _ := starredCount(db)
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"feeds":        subsToAPI(subs),
		"starredCount": starredCount,
	})
}

func (a *NewsAPI) createFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	var body struct {
		URL      string `json:"url"`
		FolderID *int64 `json:"folderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	if err := validateURL(body.URL); err != nil {
		plugin.WriteJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	// Attempt to resolve the URL to a concrete feed URL (auto-discover if HTML page).
	feedURL, err := resolveToFeedURL(r.Context(), body.URL)
	if err != nil {
		plugin.WriteJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "0",
			"data":  nil,
			"message": err.Error(),
		})
		return
	}

	var folderID sql.NullInt64
	if body.FolderID != nil {
		folderID = sql.NullInt64{Int64: *body.FolderID, Valid: true}
	}
	sub, err := createRSSSubscription(db, feedURL, folderID)
	if err != nil {
		plugin.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	go a.backend.TriggerFetch(username, sub.ID)
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"feeds": []any{subToAPI(sub)}})
}

func (a *NewsAPI) deleteFeed(w http.ResponseWriter, db *sql.DB, id int64) {
	if err := deleteSubscription(db, id); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) moveFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		FolderID *int64 `json:"folderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var folderID sql.NullInt64
	if body.FolderID != nil && *body.FolderID > 0 {
		folderID = sql.NullInt64{Int64: *body.FolderID, Valid: true}
	}
	if err := moveSubscription(db, id, folderID); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) renameFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		Title string `json:"feedTitle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "feedTitle required"})
		return
	}
	if err := renameSubscription(db, id, body.Title); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) markFeedRead(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		NewestItemID int64 `json:"newestItemId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.NewestItemID > 0 {
		if err := markSubReadBefore(db, id, body.NewestItemID); err != nil {
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	} else {
		if err := markSubRead(db, id); err != nil {
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

// --- Items (WHERE type='text') ---

func (a *NewsAPI) getItems(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	batchSize := queryInt(r, "batchSize", 50)
	offset := queryInt(r, "offset", 0)
	typ := queryInt(r, "type", 3)
	id := int64(queryInt(r, "id", 0))
	getRead := r.URL.Query().Get("getRead") == "true"

	items, err := allTextItems(db, batchSize, offset, typ, id, getRead)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"items": itemsToAPI(items)})
}

func (a *NewsAPI) getUpdatedItems(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	lastModified := int64(queryInt(r, "lastModified", 0))
	typ := queryInt(r, "type", 3)
	id := int64(queryInt(r, "id", 0))

	items, err := updatedTextItems(db, lastModified, typ, id)
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"items": itemsToAPI(items)})
}

func (a *NewsAPI) setItemRead(w http.ResponseWriter, db *sql.DB, id int64, read bool) {
	if err := setItemRead(db, id, read); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) setItemStarred(w http.ResponseWriter, db *sql.DB, id int64, starred bool) {
	if err := setItemStarred(db, id, starred); err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) bulkRead(w http.ResponseWriter, r *http.Request, db *sql.DB, read bool) {
	var body struct {
		Items []int64 `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var err error
	if read {
		err = markItemsRead(db, body.Items)
	} else {
		err = markItemsUnread(db, body.Items)
	}
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) bulkStar(w http.ResponseWriter, r *http.Request, db *sql.DB, star bool) {
	var body struct {
		Items []int64 `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	var err error
	if star {
		err = starItems(db, body.Items)
	} else {
		err = unstarItems(db, body.Items)
	}
	if err != nil {
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- DB helpers: folders ---

type folder struct {
	ID   int64
	Name string
}

func listFolders(db *sql.DB) ([]folder, error) {
	rows, err := db.Query(`SELECT id, name FROM folders ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list folders: %w", err)
	}
	defer rows.Close()
	var folders []folder
	for rows.Next() {
		var f folder
		if err := rows.Scan(&f.ID, &f.Name); err != nil {
			return nil, fmt.Errorf("list folders scan: %w", err)
		}
		folders = append(folders, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list folders rows: %w", err)
	}
	return folders, nil
}

func createFolder(db *sql.DB, name string) (folder, error) {
	res, err := db.Exec(`INSERT INTO folders (name) VALUES (?)`, name)
	if err != nil {
		return folder{}, fmt.Errorf("create folder: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return folder{}, fmt.Errorf("create folder last insert id: %w", err)
	}
	return folder{ID: id, Name: name}, nil
}

func renameFolder(db *sql.DB, id int64, name string) error {
	_, err := db.Exec(`UPDATE folders SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("rename folder: %w", err)
	}
	return nil
}

func deleteFolder(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM folders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	return nil
}

// --- DB helpers: subscriptions (kind='rss') ---

type subscription struct {
	ID         int64
	FolderID   sql.NullInt64
	URL        string
	Title      string
	FaviconURL string
	SiteURL    string
	ErrorCount int64
	LastError  string
	CreatedAt  int64
}

func listRSSSubscriptions(db *sql.DB) ([]subscription, error) {
	rows, err := db.Query(`
		SELECT id, folder_id, url, title, favicon_url, site_url, error_count, last_error, created_at
		FROM subscriptions
		WHERE kind = 'rss'
		ORDER BY title`)
	if err != nil {
		return nil, fmt.Errorf("list rss subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func createRSSSubscription(db *sql.DB, rawURL string, folderID sql.NullInt64) (subscription, error) {
	res, err := db.Exec(`
		INSERT INTO subscriptions (kind, url, folder_id)
		VALUES ('rss', ?, ?)`, rawURL, folderID)
	if err != nil {
		return subscription{}, fmt.Errorf("create rss subscription: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return subscription{}, fmt.Errorf("create rss subscription last insert id: %w", err)
	}
	// Re-fetch to get all defaults.
	var sub subscription
	row := db.QueryRow(`
		SELECT id, folder_id, url, title, favicon_url, site_url, error_count, last_error, created_at
		FROM subscriptions WHERE id = ?`, id)
	if err := row.Scan(&sub.ID, &sub.FolderID, &sub.URL, &sub.Title, &sub.FaviconURL,
		&sub.SiteURL, &sub.ErrorCount, &sub.LastError, &sub.CreatedAt); err != nil {
		return subscription{}, fmt.Errorf("create rss subscription fetch: %w", err)
	}
	return sub, nil
}

func deleteSubscription(db *sql.DB, id int64) error {
	_, err := db.Exec(`DELETE FROM subscriptions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	return nil
}

func moveSubscription(db *sql.DB, id int64, folderID sql.NullInt64) error {
	_, err := db.Exec(`UPDATE subscriptions SET folder_id = ? WHERE id = ?`, folderID, id)
	if err != nil {
		return fmt.Errorf("move subscription: %w", err)
	}
	return nil
}

func renameSubscription(db *sql.DB, id int64, title string) error {
	_, err := db.Exec(`UPDATE subscriptions SET title = ? WHERE id = ?`, title, id)
	if err != nil {
		return fmt.Errorf("rename subscription: %w", err)
	}
	return nil
}

func markSubRead(db *sql.DB, subID int64) error {
	_, err := db.Exec(`
		UPDATE items SET is_read = 1, fetched_at = unixepoch()
		WHERE subscription_id = ? AND is_read = 0`, subID)
	if err != nil {
		return fmt.Errorf("mark sub read: %w", err)
	}
	return nil
}

func markSubReadBefore(db *sql.DB, subID, newestItemID int64) error {
	_, err := db.Exec(`
		UPDATE items SET is_read = 1, fetched_at = unixepoch()
		WHERE subscription_id = ? AND id <= ? AND is_read = 0`, subID, newestItemID)
	if err != nil {
		return fmt.Errorf("mark sub read before: %w", err)
	}
	return nil
}

func scanSubscriptions(rows *sql.Rows) ([]subscription, error) {
	var subs []subscription
	for rows.Next() {
		var s subscription
		if err := rows.Scan(&s.ID, &s.FolderID, &s.URL, &s.Title, &s.FaviconURL,
			&s.SiteURL, &s.ErrorCount, &s.LastError, &s.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subs = append(subs, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan subscriptions rows: %w", err)
	}
	return subs, nil
}

// --- DB helpers: items (type='text') ---

type itemWithSub struct {
	ID          int64
	SubID       int64
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
	SubTitle    string
}

func allTextItems(db *sql.DB, limit, offset int, typ int, id int64, getRead bool) ([]itemWithSub, error) {
	q := `SELECT i.id, i.subscription_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		s.title
		FROM items i JOIN subscriptions s ON i.subscription_id = s.id
		WHERE i.type = 'text'`

	var conditions []string
	var args []any

	if !getRead {
		conditions = append(conditions, "i.is_read = 0")
	}

	switch typ {
	case 0: // subscription
		if id > 0 {
			conditions = append(conditions, "i.subscription_id = ?")
			args = append(args, id)
		}
	case 1: // folder
		if id > 0 {
			conditions = append(conditions, "s.folder_id = ?")
			args = append(args, id)
		}
	case 2: // starred
		conditions = append(conditions, "i.is_starred = 1")
	}

	if len(conditions) > 0 {
		q += " AND " + strings.Join(conditions, " AND ")
	}

	q += " ORDER BY i.id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	return queryItems(db, q, args...)
}

func updatedTextItems(db *sql.DB, lastModified int64, typ int, id int64) ([]itemWithSub, error) {
	q := `SELECT i.id, i.subscription_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred, i.content_hash,
		s.title
		FROM items i JOIN subscriptions s ON i.subscription_id = s.id
		WHERE i.type = 'text' AND i.fetched_at >= ?`
	args := []any{lastModified}

	switch typ {
	case 0: // subscription
		if id > 0 {
			q += " AND i.subscription_id = ?"
			args = append(args, id)
		}
	case 1: // folder
		if id > 0 {
			q += " AND s.folder_id = ?"
			args = append(args, id)
		}
	}

	q += " ORDER BY i.id DESC"
	return queryItems(db, q, args...)
}

func queryItems(db *sql.DB, q string, args ...any) ([]itemWithSub, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query items: %w", err)
	}
	defer rows.Close()
	var items []itemWithSub
	for rows.Next() {
		var it itemWithSub
		var isRead, isStarred int
		if err := rows.Scan(
			&it.ID, &it.SubID, &it.GUID, &it.Title, &it.Author, &it.URL,
			&it.ContentHTML, &it.PublishedAt, &it.FetchedAt, &isRead,
			&isStarred, &it.ContentHash, &it.SubTitle); err != nil {
			return nil, fmt.Errorf("scan item: %w", err)
		}
		it.IsRead = isRead != 0
		it.IsStarred = isStarred != 0
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query items rows: %w", err)
	}
	return items, nil
}

func setItemRead(db *sql.DB, id int64, read bool) error {
	_, err := db.Exec(`UPDATE items SET is_read = ?, fetched_at = unixepoch() WHERE id = ?`, boolInt(read), id)
	if err != nil {
		return fmt.Errorf("set item read: %w", err)
	}
	return nil
}

func setItemStarred(db *sql.DB, id int64, starred bool) error {
	_, err := db.Exec(`UPDATE items SET is_starred = ?, fetched_at = unixepoch() WHERE id = ?`, boolInt(starred), id)
	if err != nil {
		return fmt.Errorf("set item starred: %w", err)
	}
	return nil
}

func markItemsRead(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_read", 1, ids)
}

func markItemsUnread(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_read", 0, ids)
}

func starItems(db *sql.DB, ids []int64) error {
	return setItemsBool(db, "is_starred", 1, ids)
}

func unstarItems(db *sql.DB, ids []int64) error {
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
	q := fmt.Sprintf(`UPDATE items SET %s = ?, fetched_at = unixepoch() WHERE id IN (%s)`, column, strings.Join(placeholders, ","))
	_, err := db.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("set items bool %s=%d: %w", column, val, err)
	}
	return nil
}

func starredCount(db *sql.DB) (int, error) {
	var n int
	err := db.QueryRow(`SELECT COUNT(*) FROM items WHERE is_starred = 1`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("starred count: %w", err)
	}
	return n, nil
}

// --- API serialization ---

func folderToAPI(f folder) map[string]any {
	return map[string]any{
		"id":   f.ID,
		"name": f.Name,
	}
}

func foldersToAPI(folders []folder) []map[string]any {
	out := make([]map[string]any, len(folders))
	for i := range folders {
		out[i] = folderToAPI(folders[i])
	}
	return out
}

func subToAPI(s subscription) map[string]any {
	fid := int64(0)
	if s.FolderID.Valid {
		fid = s.FolderID.Int64
	}
	return map[string]any{
		"id":               s.ID,
		"url":              s.URL,
		"title":            s.Title,
		"faviconLink":      s.FaviconURL,
		"added":            s.CreatedAt,
		"folderId":         fid,
		"unreadCount":      0,
		"ordering":         0,
		"link":             s.SiteURL,
		"pinned":           false,
		"updateErrorCount": s.ErrorCount,
		"lastUpdateError":  s.LastError,
	}
}

func subsToAPI(subs []subscription) []map[string]any {
	out := make([]map[string]any, len(subs))
	for i := range subs {
		out[i] = subToAPI(subs[i])
	}
	return out
}

func itemToAPI(it itemWithSub) map[string]any {
	return map[string]any{
		"id":               it.ID,
		"guid":             it.GUID,
		"guidHash":         it.ContentHash,
		"url":              it.URL,
		"title":            it.Title,
		"author":           it.Author,
		"pubDate":          it.PublishedAt,
		"body":             it.ContentHTML,
		"feedId":           it.SubID,
		"unread":           !it.IsRead,
		"starred":          it.IsStarred,
		"lastModified":     it.FetchedAt,
		"fingerprint":      it.ContentHash,
		"contentHash":      it.ContentHash,
		"enclosureMime":    nil,
		"enclosureLink":    nil,
		"mediaThumbnail":   nil,
		"mediaDescription": nil,
	}
}

func itemsToAPI(items []itemWithSub) []map[string]any {
	out := make([]map[string]any, len(items))
	for i := range items {
		out[i] = itemToAPI(items[i])
	}
	return out
}

// --- Helpers ---

func queryInt(r *http.Request, key string, def int) int {
	s := r.URL.Query().Get(key)
	if s == "" {
		return def
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return v
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

var allowPrivateIPs = func() bool {
	v := os.Getenv("COG_PRIvaTE_IPS")
	return v == "true" || v == "1" || v == "yes"
}()

// validateURL checks that a URL is safe to fetch (SSRF protection).
func validateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q: only http and https are allowed", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("empty hostname")
	}
	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("resolve host: %w", err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if allowPrivateIPs {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("host %s resolves to private/loopback address %s", host, ipStr)
		}
	}
	return nil
}
