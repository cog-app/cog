package rss

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// NewsAPI implements the NextCloud News API v1-2 endpoints.
type NewsAPI struct {
	plugin *Plugin
}

// NewNewsAPI creates a new NC News API handler.
func NewNewsAPI(p *Plugin) *NewsAPI {
	return &NewsAPI{plugin: p}
}

func (a *NewsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	db, err := a.plugin.DB(u.Username)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Strip the prefix to get the API path.
	path := r.URL.Path
	for _, prefix := range []string{"/index.php/apps/news/api/v1-2", "/index.php/apps/news/api"} {
		if strings.HasPrefix(path, prefix) {
			path = strings.TrimPrefix(path, prefix)
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
		id := pathID(path, "/folders/")
		a.renameFolder(w, r, db, id)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/folders/"):
		id := pathID(path, "/folders/")
		a.deleteFolder(w, db, id)
	// Feeds
	case r.Method == "GET" && path == "/feeds":
		a.listFeeds(w, db)
	case r.Method == "POST" && path == "/feeds":
		a.createFeed(w, r, db, u.Username)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/feeds/"):
		id := pathID(path, "/feeds/")
		a.deleteFeed(w, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/move") && strings.HasPrefix(path, "/feeds/"):
		id := pathIDSuffix(path, "/feeds/", "/move")
		a.moveFeed(w, r, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/rename") && strings.HasPrefix(path, "/feeds/"):
		id := pathIDSuffix(path, "/feeds/", "/rename")
		a.renameFeed(w, r, db, id)
	case r.Method == "PUT" && strings.HasSuffix(path, "/read") && strings.HasPrefix(path, "/feeds/"):
		id := pathIDSuffix(path, "/feeds/", "/read")
		a.markFeedRead(w, r, db, id)
	// Items
	case r.Method == "GET" && path == "/items":
		a.getItems(w, r, db)
	case r.Method == "GET" && path == "/items/updated":
		a.getUpdatedItems(w, r, db)
	case r.Method == "PUT" && strings.HasSuffix(path, "/read") && strings.HasPrefix(path, "/items/"):
		id := pathIDSuffix(path, "/items/", "/read")
		a.setItemRead(w, db, id, true)
	case r.Method == "PUT" && strings.HasSuffix(path, "/unread") && strings.HasPrefix(path, "/items/"):
		id := pathIDSuffix(path, "/items/", "/unread")
		a.setItemRead(w, db, id, false)
	case r.Method == "PUT" && strings.HasSuffix(path, "/star") && strings.HasPrefix(path, "/items/"):
		id := pathIDSuffix(path, "/items/", "/star")
		a.setItemStarred(w, db, id, true)
	case r.Method == "PUT" && strings.HasSuffix(path, "/unstar") && strings.HasPrefix(path, "/items/"):
		id := pathIDSuffix(path, "/items/", "/unstar")
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
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

func (a *NewsAPI) version(w http.ResponseWriter) {
	writeJSON(w, http.StatusOK, map[string]any{
		"apiLevels": []string{"v1-2"},
	})
}

// --- Folders ---

func (a *NewsAPI) listFolders(w http.ResponseWriter, db *sql.DB) {
	folders, err := ListFolders(db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": foldersToAPI(folders)})
}

func (a *NewsAPI) createFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	folder, err := CreateFolder(db, body.Name)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": []any{folderToAPI(folder)}})
}

func (a *NewsAPI) renameFolder(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name required"})
		return
	}
	if err := RenameFolder(db, id, body.Name); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) deleteFolder(w http.ResponseWriter, db *sql.DB, id int64) {
	if err := DeleteFolder(db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Feeds ---

func (a *NewsAPI) listFeeds(w http.ResponseWriter, db *sql.DB) {
	feeds, err := ListFeeds(db)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	starredCount, _ := StarredCount(db)
	writeJSON(w, http.StatusOK, map[string]any{
		"feeds":       feedsToAPI(feeds),
		"starredCount": starredCount,
	})
}

func (a *NewsAPI) createFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, username string) {
	var body struct {
		URL      string `json:"url"`
		FolderID *int64 `json:"folderId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url required"})
		return
	}
	resolvedURL, err := ResolveURL(a.plugin.client, body.URL)
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}

	var folderID sql.NullInt64
	if body.FolderID != nil {
		folderID = sql.NullInt64{Int64: *body.FolderID, Valid: true}
	}
	feed, err := CreateFeed(db, resolvedURL, folderID)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	go a.plugin.FetchNow(username, feed.ID)
	writeJSON(w, http.StatusOK, map[string]any{"feeds": []any{feedToAPI(feed)}})
}

func (a *NewsAPI) deleteFeed(w http.ResponseWriter, db *sql.DB, id int64) {
	if err := DeleteFeed(db, id); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) moveFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		FolderID *int64 `json:"folderId"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	var folderID sql.NullInt64
	if body.FolderID != nil && *body.FolderID > 0 {
		folderID = sql.NullInt64{Int64: *body.FolderID, Valid: true}
	}
	if err := MoveFeed(db, id, folderID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) renameFeed(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		Title string `json:"feedTitle"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "feedTitle required"})
		return
	}
	if err := RenameFeed(db, id, body.Title); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) markFeedRead(w http.ResponseWriter, r *http.Request, db *sql.DB, id int64) {
	var body struct {
		NewestItemID int64 `json:"newestItemId"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if body.NewestItemID > 0 {
		MarkFeedReadBefore(db, id, body.NewestItemID)
	} else {
		MarkFeedRead(db, id)
	}
	w.WriteHeader(http.StatusOK)
}

// --- Items ---

func (a *NewsAPI) getItems(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	batchSize := queryInt(r, "batchSize", 50)
	offset := queryInt(r, "offset", 0)
	typ := queryInt(r, "type", 3)
	id := int64(queryInt(r, "id", 0))
	getRead := r.URL.Query().Get("getRead") == "true"

	items, err := AllItems(db, batchSize, offset, typ, id, getRead)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsToAPI(items)})
}

func (a *NewsAPI) getUpdatedItems(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	lastModified := int64(queryInt(r, "lastModified", 0))
	typ := queryInt(r, "type", 3)
	id := int64(queryInt(r, "id", 0))

	items, err := UpdatedItems(db, lastModified, typ, id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": itemsToAPI(items)})
}

func (a *NewsAPI) setItemRead(w http.ResponseWriter, db *sql.DB, id int64, read bool) {
	SetItemRead(db, id, read)
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) setItemStarred(w http.ResponseWriter, db *sql.DB, id int64, starred bool) {
	SetItemStarred(db, id, starred)
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) bulkRead(w http.ResponseWriter, r *http.Request, db *sql.DB, read bool) {
	var body struct {
		Items []int64 `json:"items"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if read {
		MarkItemsRead(db, body.Items)
	} else {
		MarkItemsUnread(db, body.Items)
	}
	w.WriteHeader(http.StatusOK)
}

func (a *NewsAPI) bulkStar(w http.ResponseWriter, r *http.Request, db *sql.DB, star bool) {
	var body struct {
		Items []int64 `json:"items"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	if star {
		StarItems(db, body.Items)
	} else {
		UnstarItems(db, body.Items)
	}
	w.WriteHeader(http.StatusOK)
}

// --- API serialization ---

func folderToAPI(f *Folder) map[string]any {
	return map[string]any{
		"id":   f.ID,
		"name": f.Name,
	}
}

func foldersToAPI(folders []Folder) []map[string]any {
	out := make([]map[string]any, len(folders))
	for i := range folders {
		out[i] = folderToAPI(&folders[i])
	}
	return out
}

func feedToAPI(f *Feed) map[string]any {
	fid := int64(0)
	if f.FolderID.Valid {
		fid = f.FolderID.Int64
	}
	return map[string]any{
		"id":               f.ID,
		"url":              f.URL,
		"title":            f.Title,
		"faviconLink":      f.FaviconURL,
		"added":            f.CreatedAt,
		"folderId":         fid,
		"unreadCount":      0,
		"ordering":         0,
		"link":             f.SiteURL,
		"pinned":           false,
		"updateErrorCount": f.ErrorCount,
		"lastUpdateError":  f.LastError,
	}
}

func feedsToAPI(feeds []Feed) []map[string]any {
	out := make([]map[string]any, len(feeds))
	for i := range feeds {
		out[i] = feedToAPI(&feeds[i])
	}
	return out
}

func itemToAPI(it ItemWithFeed) map[string]any {
	return map[string]any{
		"id":                 it.ID,
		"guid":               it.GUID,
		"guidHash":           it.ContentHash,
		"url":                it.URL,
		"title":              it.Title,
		"author":             it.Author,
		"pubDate":            it.PublishedAt,
		"body":               it.ContentHTML,
		"feedId":             it.FeedID,
		"unread":             !it.IsRead,
		"starred":            it.IsStarred,
		"lastModified":       it.FetchedAt,
		"fingerprint":        it.ContentHash,
		"contentHash":        it.ContentHash,
		"enclosureMime":      nil,
		"enclosureLink":      nil,
		"mediaThumbnail":     nil,
		"mediaDescription":   nil,
	}
}

func itemsToAPI(items []ItemWithFeed) []map[string]any {
	out := make([]map[string]any, len(items))
	for i := range items {
		out[i] = itemToAPI(items[i])
	}
	return out
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(data)
}

func pathID(path, prefix string) int64 {
	s := strings.TrimPrefix(path, prefix)
	// Remove trailing path segments.
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

func pathIDSuffix(path, prefix, suffix string) int64 {
	s := strings.TrimPrefix(path, prefix)
	s = strings.TrimSuffix(s, suffix)
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

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
