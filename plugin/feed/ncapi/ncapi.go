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
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/user"
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
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
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

	segs := strings.Split(strings.Trim(path, "/"), "/")
	// route is the method and path with the second segment generalised, e.g.
	// "PUT feeds/:id/move".
	route := r.Method + " " + segs[0]
	if len(segs) > 1 {
		route += "/:id"
	}
	if len(segs) > 2 {
		route += "/" + strings.Join(segs[2:], "/")
	}

	// Item updates name items in three different shapes; updateItems parses them.
	if r.Method == "PUT" && segs[0] == "items" && len(segs) > 2 {
		a.updateItems(w, r, db, segs[1:])
		return
	}

	// pathID parses the numeric ID in segs[1]. A malformed ID is a client
	// error, not ID 0: treating it as 0 used to turn bad requests into
	// silent no-ops that still answered 200.
	pathID := func() (int64, bool) {
		id, err := strconv.ParseInt(segs[1], 10, 64)
		if err != nil || id <= 0 {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id " + strconv.Quote(segs[1])})
			return 0, false
		}
		return id, true
	}

	switch route {
	case "GET ":
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"apiLevels": []string{"v1-2"}})
	case "GET version":
		plugin.WriteJSON(w, http.StatusOK, map[string]any{"version": serverVersion})
	case "GET status":
		plugin.WriteJSON(w, http.StatusOK, map[string]any{
			"version": serverVersion,
			"warnings": map[string]bool{
				"improperlyConfiguredCron": false,
				"incorrectDbCharset":       false,
			},
		})
	case "GET user":
		a.user(w, u)
	// Folders
	case "GET folders":
		a.listFolders(w, r, db)
	case "POST folders":
		a.createFolder(w, r, db)
	case "PUT folders/:id":
		if id, ok := pathID(); ok {
			a.renameFolder(w, r, db, id)
		}
	case "PUT folders/:id/read":
		if id, ok := pathID(); ok {
			a.markFolderRead(w, r, db, id)
		}
	case "DELETE folders/:id":
		if id, ok := pathID(); ok {
			a.deleteFolder(w, db, id)
		}
	// Feeds (subscriptions WHERE kind='rss')
	case "GET feeds":
		a.listFeeds(w, r, db)
	case "POST feeds":
		a.createFeed(w, r, db, u.Username)
	case "DELETE feeds/:id":
		if id, ok := pathID(); ok {
			a.deleteFeed(w, db, id)
		}
	case "PUT feeds/:id/move":
		if id, ok := pathID(); ok {
			a.moveFeed(w, r, db, id)
		}
	case "PUT feeds/:id/rename":
		if id, ok := pathID(); ok {
			a.renameFeed(w, r, db, id)
		}
	case "PUT feeds/:id/read":
		if id, ok := pathID(); ok {
			a.markFeedRead(w, r, db, id)
		}
	// Items (WHERE type='text')
	case "GET items":
		a.getItems(w, r, db)
	case "GET items/:id":
		if segs[1] != "updated" {
			plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		a.getUpdatedItems(w, r, db)
	case "PUT items/:id":
		if segs[1] != "read" {
			plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		a.markAllRead(w, r, db)
	default:
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// serverVersion is the NextCloud News version reported by /version and
// /status. Clients may gate features on it; apiLevels, not this, says which
// API is implemented.
const serverVersion = "18.0.0"

// user answers GET /user, which clients use to label the account.
func (a *NewsAPI) user(w http.ResponseWriter, u *user.User) {
	name := u.DisplayName
	if name == "" {
		name = u.Username
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"userId":             u.Username,
		"displayName":        name,
		"lastLoginTimestamp": 0,
		"avatar":             nil,
	})
}

// --- Folders ---

func (a *NewsAPI) listFolders(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	folders, err := listFolders(db)
	if err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
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
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) deleteFolder(w http.ResponseWriter, db *sql.DB, id int64) {
	if err := deleteFolder(db, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Feeds (subscriptions WHERE kind='rss') ---

func (a *NewsAPI) listFeeds(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	subs, err := listRSSSubscriptions(db)
	if err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	starred, newest, err := feedTotals(db)
	if err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	resp := map[string]any{
		"feeds":        subsToAPI(subs),
		"starredCount": starred,
	}
	if newest > 0 {
		resp["newestItemId"] = newest
	}
	plugin.WriteJSON(w, http.StatusOK, resp)
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
			"error":   "0",
			"data":    nil,
			"message": err.Error(),
		})
		return
	}

	var folderID sql.NullInt64
	if body.FolderID != nil && *body.FolderID > 0 {
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
		writeErr(w, err)
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
		writeErr(w, err)
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
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) markFeedRead(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	a.markReadWhere(w, r, db, "subscription_id = ?", id)
}

// --- Items (WHERE type='text') ---

func (a *NewsAPI) getItems(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	batchSize := queryInt(r, "batchSize", 50)
	offset := int64(queryInt(r, "offset", 0))
	typ := queryInt(r, "type", 3)
	id := int64(queryInt(r, "id", 0))
	getRead := r.URL.Query().Get("getRead") != "false"
	oldestFirst := r.URL.Query().Get("oldestFirst") == "true"

	items, err := allTextItems(db, batchSize, offset, typ, id, getRead, oldestFirst)
	if err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
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
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	plugin.WriteJSON(w, http.StatusOK, map[string]any{"items": itemsToAPI(items)})
}

// itemActions maps the verb at the end of an item path to the column it sets.
var itemActions = map[string]struct {
	column string
	value  int
}{
	"read":   {"is_read", 1},
	"unread": {"is_read", 0},
	"star":   {"is_starred", 1},
	"unstar": {"is_starred", 0},
}

// updateItems handles every PUT under /items/ that changes read or starred
// state. segs is the path after "/items/", in one of three shapes:
//
//	{itemId}/{action}            v1-2 read/unread, v1-3 everything
//	{feedId}/{guidHash}/{action} v1-2 star/unstar
//	{action}/multiple            bulk; the body names the items
func (a *NewsAPI) updateItems(w http.ResponseWriter, r *http.Request, db *sql.DB, segs []string) {
	act, ok := itemActions[segs[len(segs)-1]]
	if len(segs) == 2 && segs[1] == "multiple" {
		act, ok = itemActions[segs[0]]
	}
	if !ok || len(segs) < 2 || len(segs) > 3 {
		plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}

	var ids []int64
	switch {
	case segs[1] == "multiple":
		var body struct {
			Items   json.RawMessage `json:"items"`
			ItemIDs []int64         `json:"itemIds"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}
		refs, err := decodeItemRefs(body.Items)
		if err != nil {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ids = append(body.ItemIDs, refs.ids...)
		byHash, err := itemIDsByGUIDHash(db, refs.hashes)
		if err != nil {
			httperr.Log(r, err)
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
			return
		}
		ids = append(ids, byHash...)
	case len(segs) == 3:
		feedID, err := strconv.ParseInt(segs[0], 10, 64)
		if err != nil {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid feed id"})
			return
		}
		ids, err = itemIDsByGUIDHash(db, []guidRef{{FeedID: feedID, GUIDHash: segs[1]}})
		if err != nil {
			httperr.Log(r, err)
			plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
			return
		}
		if len(ids) == 0 {
			plugin.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "item not found"})
			return
		}
	default:
		id, err := strconv.ParseInt(segs[0], 10, 64)
		if err != nil {
			plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid item id"})
			return
		}
		ids = []int64{id}
	}

	if err := setItemsBool(db, act.column, act.value, ids); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// markAllRead handles PUT /items/read.
func (a *NewsAPI) markAllRead(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	a.markReadWhere(w, r, db, "1 = 1")
}

// markFolderRead handles PUT /folders/{id}/read.
func (a *NewsAPI) markFolderRead(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	a.markReadWhere(w, r, db, "subscription_id IN (SELECT id FROM subscriptions WHERE folder_id = ?)", id)
}

// markReadWhere marks the text items matching cond read, up to the body's
// newestItemId so that items the client has not seen yet stay unread.
func (a *NewsAPI) markReadWhere(w http.ResponseWriter, r *http.Request, db *sql.DB, cond string, args ...any) {
	var body struct {
		NewestItemID int64 `json:"newestItemId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		plugin.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}
	if body.NewestItemID > 0 {
		cond += " AND id <= ?"
		args = append(args, body.NewestItemID)
	}
	if err := markItemsReadWhere(db, cond, args...); err != nil {
		httperr.Log(r, err)
		plugin.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": httperr.Message})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// guidRef is how API v1-2 names an item for starring.
type guidRef struct {
	FeedID   int64  `json:"feedId"`
	GUIDHash string `json:"guidHash"`
}

type itemRefs struct {
	ids    []int64
	hashes []guidRef
}

// decodeItemRefs reads a bulk "items" array, which v1-2 sends as item IDs for
// read/unread and as {feedId, guidHash} objects for star/unstar.
func decodeItemRefs(raw json.RawMessage) (itemRefs, error) {
	var refs itemRefs
	if len(raw) == 0 || string(raw) == "null" {
		return refs, nil
	}
	if err := json.Unmarshal(raw, &refs.ids); err == nil {
		return refs, nil
	}
	if err := json.Unmarshal(raw, &refs.hashes); err != nil {
		return refs, fmt.Errorf("items must be item ids or {feedId, guidHash} objects: %w", err)
	}
	return refs, nil
}

// itemIDsByGUIDHash resolves v1-2 {feedId, guidHash} references. The hash is
// derived rather than stored, so each feed's GUIDs are hashed once per call.
func itemIDsByGUIDHash(db *sql.DB, refs []guidRef) ([]int64, error) {
	want := make(map[int64]map[string]bool)
	for _, ref := range refs {
		if want[ref.FeedID] == nil {
			want[ref.FeedID] = make(map[string]bool)
		}
		want[ref.FeedID][ref.GUIDHash] = true
	}
	var ids []int64
	for feedID, hashes := range want {
		rows, err := db.Query(`SELECT id, guid FROM items WHERE subscription_id = ? AND type = 'text'`, feedID)
		if err != nil {
			return nil, fmt.Errorf("items by guid hash: %w", err)
		}
		for rows.Next() {
			var id int64
			var guid string
			if err := rows.Scan(&id, &guid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("items by guid hash scan: %w", err)
			}
			if hashes[md5Hex(guid)] {
				ids = append(ids, id)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("items by guid hash rows: %w", err)
		}
	}
	return ids, nil
}

// errNotFound is returned by the DB helpers when no row matched the ID.
var errNotFound = errors.New("not found")

// writeErr answers with 404 for errNotFound and 500 for anything else.
func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	if errors.Is(err, errNotFound) {
		status = http.StatusNotFound
	}
	plugin.WriteJSON(w, status, map[string]string{"error": err.Error()})
}

// execOne runs a statement that targets one row and reports errNotFound
// when it matched none.
func execOne(db *sql.DB, what, q string, args ...any) error {
	res, err := db.Exec(q, args...)
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s rows affected: %w", what, err)
	}
	if n == 0 {
		return fmt.Errorf("%s: %w", what, errNotFound)
	}
	return nil
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
	return execOne(db, "rename folder", `UPDATE folders SET name = ? WHERE id = ?`, name, id)
}

// deleteFolder deletes a folder and, as NextCloud News does, the RSS feeds in
// it. Podcast and video subscriptions are not the news API's to delete, so
// they fall back to the root.
func deleteFolder(db *sql.DB, id int64) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("delete folder begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM subscriptions WHERE folder_id = ? AND kind = 'rss'`, id); err != nil {
		return fmt.Errorf("delete folder feeds: %w", err)
	}
	res, err := tx.Exec(`DELETE FROM folders WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete folder: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete folder rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("delete folder: %w", errNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("delete folder commit: %w", err)
	}
	return nil
}

// --- DB helpers: subscriptions (kind='rss') ---

type subscription struct {
	ID          int64
	FolderID    sql.NullInt64
	URL         string
	Title       string
	FaviconURL  string
	SiteURL     string
	ErrorCount  int64
	LastError   string
	CreatedAt   int64
	UnreadCount int64
}

func listRSSSubscriptions(db *sql.DB) ([]subscription, error) {
	rows, err := db.Query(`
		SELECT id, folder_id, url, title, favicon_url, site_url, error_count, last_error, created_at,
			(SELECT COUNT(*) FROM items i WHERE i.subscription_id = s.id AND i.type = 'text' AND i.is_read = 0)
		FROM subscriptions s
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
	return execOne(db, "delete subscription", `DELETE FROM subscriptions WHERE id = ? AND kind = 'rss'`, id)
}

func moveSubscription(db *sql.DB, id int64, folderID sql.NullInt64) error {
	return execOne(db, "move subscription", `
		UPDATE subscriptions SET folder_id = ?1
		WHERE id = ?2 AND kind = 'rss' AND (?1 IS NULL OR ?1 IN (SELECT id FROM folders))`, folderID, id)
}

func renameSubscription(db *sql.DB, id int64, title string) error {
	return execOne(db, "rename subscription", `UPDATE subscriptions SET title = ? WHERE id = ? AND kind = 'rss'`, title, id)
}

func scanSubscriptions(rows *sql.Rows) ([]subscription, error) {
	var subs []subscription
	for rows.Next() {
		var s subscription
		if err := rows.Scan(&s.ID, &s.FolderID, &s.URL, &s.Title, &s.FaviconURL,
			&s.SiteURL, &s.ErrorCount, &s.LastError, &s.CreatedAt, &s.UnreadCount); err != nil {
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
	SubTitle    string
}

// allTextItems pages through text items the way NextCloud News does: offset
// is an item ID, not a row count, and each page continues past the last ID
// the client received.
func allTextItems(db *sql.DB, limit int, offset int64, typ int, id int64, getRead, oldestFirst bool) ([]itemWithSub, error) {
	q := `SELECT i.id, i.subscription_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred,
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

	order := "DESC"
	if oldestFirst {
		order = "ASC"
	}
	if offset > 0 {
		if oldestFirst {
			conditions = append(conditions, "i.id > ?")
		} else {
			conditions = append(conditions, "i.id < ?")
		}
		args = append(args, offset)
	}

	if len(conditions) > 0 {
		q += " AND " + strings.Join(conditions, " AND ")
	}

	q += " ORDER BY i.id " + order + " LIMIT ?"
	args = append(args, limit)

	return queryItems(db, q, args...)
}

func updatedTextItems(db *sql.DB, lastModified int64, typ int, id int64) ([]itemWithSub, error) {
	q := `SELECT i.id, i.subscription_id, i.guid, i.title, i.author, i.url,
		i.content_html, i.published_at, i.fetched_at, i.is_read, i.is_starred,
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
	case 2: // starred
		q += " AND i.is_starred = 1"
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
			&isStarred, &it.SubTitle); err != nil {
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

// markItemsReadWhere marks unread text items matching cond read.
func markItemsReadWhere(db *sql.DB, cond string, args ...any) error {
	_, err := db.Exec(`UPDATE items SET is_read = 1, fetched_at = unixepoch()
		WHERE type = 'text' AND is_read = 0 AND `+cond, args...)
	if err != nil {
		return fmt.Errorf("mark items read: %w", err)
	}
	return nil
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

// feedTotals returns the starred count and newest item ID that accompany the
// feed list.
func feedTotals(db *sql.DB) (starred, newest int64, err error) {
	err = db.QueryRow(`
		SELECT COALESCE(SUM(is_starred), 0), COALESCE(MAX(id), 0)
		FROM items WHERE type = 'text'`).Scan(&starred, &newest)
	if err != nil {
		return 0, 0, fmt.Errorf("feed totals: %w", err)
	}
	return starred, newest, nil
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
		"unreadCount":      s.UnreadCount,
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

// itemToAPI serialises an item. NextCloud News derives guidHash from the GUID
// and fingerprint from the content; clients key and deduplicate on both, so
// each must differ between items exactly when NextCloud's would.
func itemToAPI(it itemWithSub) map[string]any {
	fingerprint := md5Hex(it.Title + it.URL + it.ContentHTML)
	return map[string]any{
		"id":               it.ID,
		"guid":             it.GUID,
		"guidHash":         md5Hex(it.GUID),
		"url":              it.URL,
		"title":            it.Title,
		"author":           it.Author,
		"pubDate":          it.PublishedAt,
		"body":             it.ContentHTML,
		"feedId":           it.SubID,
		"unread":           !it.IsRead,
		"starred":          it.IsStarred,
		"lastModified":     it.FetchedAt,
		"fingerprint":      fingerprint,
		"contentHash":      fingerprint,
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

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

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
		if safehttp.AllowPrivate() {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("host %s resolves to private/loopback address %s", host, ipStr)
		}
	}
	return nil
}
