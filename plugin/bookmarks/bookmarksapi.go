package bookmarks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// BookmarksAPI implements the NextCloud Bookmarks REST API v2 endpoints.
type BookmarksAPI struct {
	plugin *Plugin
}

// NewBookmarksAPI creates a new NC Bookmarks API handler.
func NewBookmarksAPI(p *Plugin) *BookmarksAPI {
	return &BookmarksAPI{plugin: p}
}

func (a *BookmarksAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		apiJSON(w, http.StatusUnauthorized, apiErr("unauthorized"))
		return
	}

	db, err := a.plugin.DB(u.Username)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}

	path := r.URL.Path
	for _, prefix := range []string{
		"/index.php/apps/bookmarks/public/rest/v2",
	} {
		if after, ok := strings.CutPrefix(path, prefix); ok {
			path = after
			break
		}
	}
	if path == "" {
		path = "/"
	}

	switch {
	// Bookmark endpoints
	case r.Method == "GET" && path == "/bookmark":
		a.listBookmarks(w, r, db)
	case r.Method == "POST" && path == "/bookmark":
		a.createBookmark(w, r, db)
	case r.Method == "GET" && pathMatch(path, "/bookmark/") && !strings.Contains(pathAfter(path, "/bookmark/"), "/"):
		a.getBookmark(w, r, db)
	case r.Method == "PUT" && pathMatch(path, "/bookmark/") && !strings.Contains(pathAfter(path, "/bookmark/"), "/"):
		a.updateBookmark(w, r, db)
	case r.Method == "DELETE" && pathMatch(path, "/bookmark/") && !strings.Contains(pathAfter(path, "/bookmark/"), "/"):
		a.deleteBookmark(w, r, db)
	case r.Method == "POST" && path == "/bookmark/click":
		a.recordClick(w, r, db)

	// Folder endpoints
	case r.Method == "GET" && path == "/folder":
		a.listFolders(w, r, db)
	case r.Method == "POST" && path == "/folder":
		a.createFolder(w, r, db)
	case r.Method == "GET" && pathMatch(path, "/folder/") && !strings.Contains(pathAfter(path, "/folder/"), "/"):
		a.getFolder(w, r, db)
	case r.Method == "PUT" && pathMatch(path, "/folder/") && !strings.Contains(pathAfter(path, "/folder/"), "/"):
		a.updateFolder(w, r, db)
	case r.Method == "DELETE" && pathMatch(path, "/folder/") && !strings.Contains(pathAfter(path, "/folder/"), "/"):
		a.deleteFolder(w, r, db)

	// Tag endpoints
	case r.Method == "GET" && path == "/tag":
		a.listTags(w, r, db)
	case r.Method == "PUT" && path == "/tag":
		a.renameTag(w, r, db)
	case r.Method == "DELETE" && path == "/tag":
		a.deleteTag(w, r, db)

	default:
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
	}
}

// --- Bookmark handlers ---

func (a *BookmarksAPI) listBookmarks(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	q := r.URL.Query()

	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 {
		limit = 10
	}
	sortby := q.Get("sortby")
	if sortby == "" {
		sortby = "lastmodified"
	}

	// Collect search terms and tags.
	searches := q["search[]"]
	search := strings.Join(searches, " ")
	tags := q["tags[]"]

	// folder filter
	var folderID *int64
	if fstr := q.Get("folder"); fstr != "" {
		if fid, err := strconv.ParseInt(fstr, 10, 64); err == nil {
			folderID = &fid
		}
	}

	// Exact URL lookup.
	if urlQ := q.Get("url"); urlQ != "" {
		bm, err := GetBookmarkByURL(db, urlQ)
		if errors.Is(err, sql.ErrNoRows) {
			apiJSON(w, http.StatusOK, apiResp("bookmarks", []any{}))
			return
		}
		if err != nil {
			a.serverError(w, "find bookmark by url", err)
			return
		}
		apiJSON(w, http.StatusOK, apiResp("bookmarks", []any{bookmarkToAPI(bm)}))
		return
	}

	bms, _, err := ListBookmarks(db, folderID, tags, search, sortby, page, limit)
	if err != nil {
		a.serverError(w, "list bookmarks", err)
		return
	}
	out := make([]any, len(bms))
	for i := range bms {
		out[i] = bookmarkToAPI(&bms[i])
	}
	apiJSON(w, http.StatusOK, apiResp("bookmarks", out))
}

func (a *BookmarksAPI) createBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Folders     []int64  `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	if body.URL == "" {
		apiJSON(w, http.StatusBadRequest, apiErr("url is required"))
		return
	}

	bm, err := CreateBookmark(db, body.URL, body.Title, body.Description, body.Tags, body.Folders)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) getBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/bookmark/")
	bm, err := GetBookmark(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
		return
	}
	if err != nil {
		a.serverError(w, "get bookmark", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) updateBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/bookmark/")

	var body struct {
		URL         *string  `json:"url"`
		Title       *string  `json:"title"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		Folders     []int64  `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}

	bm, err := UpdateBookmark(db, id, body.Title, body.Description, body.Tags, body.Folders)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) deleteBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/bookmark/")
	if err := DeleteBookmark(db, id); err != nil {
		a.serverError(w, "delete bookmark", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

func (a *BookmarksAPI) recordClick(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	if err := RecordClick(db, body.URL); err != nil {
		a.serverError(w, "record click", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// --- Folder handlers ---

func (a *BookmarksAPI) listFolders(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	folders, err := ListFolders(db)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	out := make([]any, len(folders))
	for i := range folders {
		out[i] = folderToAPI(&folders[i])
	}
	apiJSON(w, http.StatusOK, apiResp("data", out))
}

func (a *BookmarksAPI) createFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Title        string `json:"title"`
		ParentFolder *int64 `json:"parent_folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	f, err := CreateFolder(db, body.Title, body.ParentFolder)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

func (a *BookmarksAPI) getFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/folder/")
	f, err := GetFolder(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
		return
	}
	if err != nil {
		a.serverError(w, "get folder", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

func (a *BookmarksAPI) updateFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/folder/")
	var body struct {
		Title        *string `json:"title"`
		ParentFolder *int64  `json:"parent_folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	title := ""
	if body.Title != nil {
		title = *body.Title
	}
	if err := UpdateFolder(db, id, title, body.ParentFolder); err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	f, err := GetFolder(db, id)
	if errors.Is(err, sql.ErrNoRows) {
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
		return
	}
	if err != nil {
		a.serverError(w, "get folder", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

func (a *BookmarksAPI) deleteFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id := apiPathID(r.URL.Path, "/folder/")
	if err := DeleteFolder(db, id); err != nil {
		a.serverError(w, "delete folder", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// --- Tag handlers ---

func (a *BookmarksAPI) listTags(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	tags, err := ListAllTags(db)
	if err != nil {
		apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
		return
	}
	apiJSON(w, http.StatusOK, tags)
}

func (a *BookmarksAPI) renameTag(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	if err := RenameTag(db, body.Old, body.New); err != nil {
		a.serverError(w, "rename tag", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

func (a *BookmarksAPI) deleteTag(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Old string `json:"old"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	if err := DeleteTag(db, body.Old); err != nil {
		a.serverError(w, "delete tag", err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// --- API helpers ---

// serverError logs err and answers 500 in the API's error shape.
func (a *BookmarksAPI) serverError(w http.ResponseWriter, what string, err error) {
	a.plugin.log.Error("bookmarks api: "+what, "err", err)
	apiJSON(w, http.StatusInternalServerError, apiErr(err.Error()))
}

func bookmarkToAPI(bm *Bookmark) map[string]any {
	tags := bm.Tags
	if tags == nil {
		tags = []string{}
	}
	folders := bm.Folders
	if folders == nil {
		folders = []int64{}
	}
	return map[string]any{
		"id":           bm.ID,
		"url":          bm.URL,
		"title":        bm.Title,
		"description":  bm.Description,
		"added":        bm.CreatedAt,
		"lastmodified": bm.UpdatedAt,
		"clickcount":   bm.ClickCount,
		"available":    bm.Available,
		"tags":         tags,
		"folders":      folders,
	}
}

func folderToAPI(f *Folder) map[string]any {
	parentFolder := int64(-1)
	if f.ParentID != nil {
		parentFolder = *f.ParentID
	}
	return map[string]any{
		"id":            f.ID,
		"title":         f.Title,
		"parent_folder": parentFolder,
	}
}

func apiJSON(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(data)
}

func apiErr(msg string) map[string]any {
	return map[string]any{"status": "error", "message": msg}
}

func apiResp(key string, data any) map[string]any {
	return map[string]any{"status": "success", key: data}
}

func apiPathID(path, prefix string) int64 {
	// Strip everything before the prefix (including version path).
	if i := strings.LastIndex(path, prefix); i >= 0 {
		path = path[i:]
	}
	s := strings.TrimPrefix(path, prefix)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

func pathMatch(path, prefix string) bool {
	return strings.HasPrefix(path, prefix) && len(path) > len(prefix)
}

func pathAfter(path, prefix string) string {
	return strings.TrimPrefix(path, prefix)
}
