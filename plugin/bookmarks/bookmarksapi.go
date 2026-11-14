package bookmarks

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httperr"
)

// apiPrefix is where the NextCloud Bookmarks REST API v2 is served.
const apiPrefix = "/index.php/apps/bookmarks/public/rest/v2"

// maxImportSize bounds a bookmarks file sent to the import endpoint.
const maxImportSize = 32 << 20

// BookmarksAPI implements the NextCloud Bookmarks REST API v2 endpoints,
// including those Floccus needs to sync: the lock, folder children, child
// order, folder hashes and bulk import.
type BookmarksAPI struct {
	plugin *Plugin
	mux    *http.ServeMux
}

// apiHandler handles one API request for the user whose database is db.
type apiHandler func(w http.ResponseWriter, r *http.Request, db *sql.DB)

// NewBookmarksAPI creates a new NC Bookmarks API handler.
func NewBookmarksAPI(p *Plugin) *BookmarksAPI {
	a := &BookmarksAPI{plugin: p, mux: http.NewServeMux()}
	routes := map[string]apiHandler{
		"GET /bookmark":               a.listBookmarks,
		"POST /bookmark":              a.createBookmark,
		"GET /bookmark/{id}":          a.getBookmark,
		"PUT /bookmark/{id}":          a.updateBookmark,
		"DELETE /bookmark/{id}":       a.deleteBookmark,
		"POST /bookmark/click":        a.recordClick,
		"POST /bookmark/import":       a.importBookmarks,
		"GET /folder":                 a.listFolders,
		"POST /folder":                a.createFolder,
		"GET /folder/{id}":            a.getFolder,
		"PUT /folder/{id}":            a.updateFolder,
		"DELETE /folder/{id}":         a.deleteFolder,
		"GET /folder/{id}/children":   a.folderChildren,
		"GET /folder/{id}/childorder": a.getChildOrder,
		// NextCloud Bookmarks routes PATCH; Floccus has sent POST.
		"PATCH /folder/{id}/childorder":       a.setChildOrder,
		"POST /folder/{id}/childorder":        a.setChildOrder,
		"GET /folder/{id}/hash":               a.folderHash,
		"POST /folder/{id}/bookmarks/{bid}":   a.addToFolder,
		"DELETE /folder/{id}/bookmarks/{bid}": a.removeFromFolder,
		"POST /folder/{id}/import":            a.importBookmarks,
		"POST /lock":                          a.acquireLock,
		"DELETE /lock":                        a.releaseLock,
		"GET /tag":                            a.listTags,
		"PUT /tag":                            a.renameTag,
		"DELETE /tag":                         a.deleteTag,
	}
	for pattern, h := range routes {
		a.mux.Handle(pattern, a.withDB(h))
	}
	a.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
	})
	return a
}

func (a *BookmarksAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	http.StripPrefix(apiPrefix, a.mux).ServeHTTP(w, r)
}

// withDB answers 401 without a user, and otherwise calls h with their database.
func (a *BookmarksAPI) withDB(h apiHandler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil {
			apiJSON(w, http.StatusUnauthorized, apiErr("unauthorized"))
			return
		}
		db, err := a.plugin.DB(u.Username)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		h(w, r, db)
	})
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
			apiJSON(w, http.StatusOK, apiResp("data", []any{}))
			return
		}
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		apiJSON(w, http.StatusOK, apiResp("data", []any{bookmarkToAPI(bm)}))
		return
	}

	bms, _, err := ListBookmarks(db, folderID, tags, search, sortby, page, limit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	out := make([]any, len(bms))
	for i := range bms {
		out[i] = bookmarkToAPI(&bms[i])
	}
	apiJSON(w, http.StatusOK, apiResp("data", out))
}

func (a *BookmarksAPI) createBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		URL         string   `json:"url"`
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Tags        []string `json:"tags"`
		Folders     flexIDs  `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	if body.URL == "" {
		apiJSON(w, http.StatusBadRequest, apiErr("url is required"))
		return
	}

	// A URL that is already saved gains the folders rather than moving to
	// them, as in NextCloud Bookmarks: a sync client that has the same link
	// in two folders creates it once for each, and expects to find it in both.
	existing, err := GetBookmarkByURL(db, body.URL)
	var bm *Bookmark
	switch {
	case err == nil:
		folders := existing.Folders
		for _, f := range body.Folders.int64s() {
			if !slices.Contains(folders, f) {
				folders = append(folders, f)
			}
		}
		var title, desc *string
		if body.Title != "" {
			title = &body.Title
		}
		if body.Description != "" {
			desc = &body.Description
		}
		bm, err = UpdateBookmark(db, existing.ID, nil, title, desc, body.Tags, folders)
	case errors.Is(err, sql.ErrNoRows):
		bm, err = CreateBookmark(db, body.URL, body.Title, body.Description, body.Tags, body.Folders.int64s())
	}
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) getBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	bm, err := GetBookmark(db, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) updateBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		URL         *string  `json:"url"`
		Target      *string  `json:"target"`
		Title       *string  `json:"title"`
		Description *string  `json:"description"`
		Tags        []string `json:"tags"`
		Folders     flexIDs  `json:"folders"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	url := body.URL
	if url == nil {
		url = body.Target
	}
	if url != nil && *url == "" {
		url = nil
	}
	if _, err := GetBookmark(db, id); err != nil {
		a.storeError(w, r, err)
		return
	}
	bm, err := UpdateBookmark(db, id, url, body.Title, body.Description, body.Tags, body.Folders.int64s())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", bookmarkToAPI(bm)))
}

func (a *BookmarksAPI) deleteBookmark(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := DeleteBookmark(db, id); err != nil {
		a.serverError(w, r, err)
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
		a.serverError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// importBookmarks adds a Netscape bookmarks file, sent as the bm_import part of
// a multipart form, to a folder (the root without one), keeping its folders.
// The answer is what was imported, as a children listing.
func (a *BookmarksAPI) importBookmarks(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	folder := RootFolder
	if r.PathValue("id") != "" {
		var ok bool
		if folder, ok = pathInt(w, r, "id"); !ok {
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxImportSize)
	f, _, err := r.FormFile("bm_import")
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("bm_import file is required"))
		return
	}
	defer f.Close()
	refs, err := ImportNetscapeTree(db, folder, f)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	t, err := LoadTree(db, folder, -1)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	out := make([]any, len(refs))
	for i, c := range refs {
		out[i] = childToAPI(t, c, -1)
	}
	apiJSON(w, http.StatusOK, apiResp("data", out))
}

// --- Folder handlers ---

// listFolders answers the folder hierarchy under ?root= (the root folder by
// default), ?layers= levels deep (all of them by default), without bookmarks.
func (a *BookmarksAPI) listFolders(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	root, layers, ok := queryRootLayers(w, r, "root", -1)
	if !ok {
		return
	}
	t, err := LoadTree(db, root, 0)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("data", subfoldersToAPI(t, root, layers)))
}

func (a *BookmarksAPI) createFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	var body struct {
		Title        string  `json:"title"`
		ParentFolder *flexID `json:"parent_folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	f, err := CreateFolder(db, body.Title, parentPtr(body.ParentFolder))
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

func (a *BookmarksAPI) getFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if id == RootFolder {
		apiJSON(w, http.StatusOK, apiResp("item", map[string]any{"id": RootFolder, "title": "", "parent_folder": RootFolder}))
		return
	}
	f, err := GetFolder(db, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

// updateFolder renames and moves a folder. A field left out keeps its value;
// parent_folder -1 is the root.
func (a *BookmarksAPI) updateFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Title        *string `json:"title"`
		ParentFolder *flexID `json:"parent_folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	f, err := GetFolder(db, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	title, parent := f.Title, f.ParentID
	if body.Title != nil {
		title = *body.Title
	}
	if body.ParentFolder != nil {
		parent = parentPtr(body.ParentFolder)
	}
	if err := UpdateFolder(db, id, title, parent); err != nil {
		a.storeError(w, r, err)
		return
	}
	if f, err = GetFolder(db, id); err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("item", folderToAPI(f)))
}

func (a *BookmarksAPI) deleteFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := DeleteFolderTree(db, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// folderChildren lists a folder's folders and bookmarks in order. A folder
// fewer than ?layers= levels down lists its own children too; one at the last
// level has no "children" field, which tells Floccus to fetch them later.
func (a *BookmarksAPI) folderChildren(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, layers, ok := a.folderLayers(w, r)
	if !ok {
		return
	}
	t, err := LoadTree(db, id, layers)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	cs := t.Children(id)
	out := make([]any, len(cs))
	for i, c := range cs {
		out[i] = childToAPI(t, c, layers-1)
	}
	apiJSON(w, http.StatusOK, apiResp("data", out))
}

func (a *BookmarksAPI) getChildOrder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, layers, ok := a.folderLayers(w, r)
	if !ok {
		return
	}
	t, err := LoadTree(db, id, layers)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("data", childOrderToAPI(t, id, layers)))
}

func (a *BookmarksAPI) setChildOrder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Data []struct {
			Type ChildType `json:"type"`
			ID   flexID    `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid body"))
		return
	}
	order := make([]ChildRef, len(body.Data))
	for i, c := range body.Data {
		if c.Type != ChildFolder && c.Type != ChildBookmark {
			apiJSON(w, http.StatusBadRequest, apiErr("a child item is missing properties"))
			return
		}
		order[i] = ChildRef{Type: c.Type, ID: int64(c.ID)}
	}
	if err := SetChildOrder(db, id, order); err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

// folderHash answers a hash of the folder and everything in it, computed as
// FolderHash describes, with ?hashFn= and the bookmark ?fields[]= to cover.
func (a *BookmarksAPI) folderHash(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	q := r.URL.Query()
	hashFn := q.Get("hashFn")
	if hashFn == "" {
		hashFn = "sha256"
	}
	fields := q["fields[]"]
	if len(fields) == 0 {
		fields = []string{"title", "url"}
	}
	t, err := LoadTree(db, id, -1)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	h, err := t.FolderHash(id, fields, hashFn)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr(err.Error()))
		return
	}
	apiJSON(w, http.StatusOK, apiResp("data", h))
}

func (a *BookmarksAPI) addToFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	folder, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	bm, ok := pathInt(w, r, "bid")
	if !ok {
		return
	}
	if err := AddToFolder(db, bm, folder); err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

// removeFromFolder takes a bookmark out of a folder, deleting it if it is in
// no other. This is how Floccus deletes a bookmark.
func (a *BookmarksAPI) removeFromFolder(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	folder, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	bm, ok := pathInt(w, r, "bid")
	if !ok {
		return
	}
	if err := RemoveFromFolder(db, bm, folder); err != nil {
		a.storeError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

// --- Sync lock ---

// acquireLock takes the user's sync lock, or answers 423 Locked while another
// client holds it. Floccus takes it for the length of a sync and renews it
// every two minutes, so two browsers do not sync at once.
func (a *BookmarksAPI) acquireLock(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	ok, err := AcquireSyncLock(db, a.plugin.ctx.Now())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !ok {
		apiJSON(w, http.StatusLocked, map[string]any{"status": "error", "data": []string{"Resource is already locked"}})
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

func (a *BookmarksAPI) releaseLock(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if err := ReleaseSyncLock(db); err != nil {
		a.serverError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, map[string]any{"status": "success"})
}

// --- Tag handlers ---

func (a *BookmarksAPI) listTags(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	tags, err := ListAllTags(db)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if tags == nil {
		tags = []string{}
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
		a.serverError(w, r, err)
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
		a.serverError(w, r, err)
		return
	}
	apiJSON(w, http.StatusOK, apiResp("status", "success"))
}

// --- API helpers ---

// serverError logs err and answers 500 in the API's error shape.
func (a *BookmarksAPI) serverError(w http.ResponseWriter, r *http.Request, err error) {
	httperr.Log(r, err)
	apiJSON(w, http.StatusInternalServerError, apiErr(httperr.Message))
}

// storeError answers an error from the store: the client's mistakes with
// their status, anything else as a server error.
func (a *BookmarksAPI) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, ErrFolderNotFound):
		apiJSON(w, http.StatusNotFound, apiErr("not found"))
	case errors.Is(err, ErrDuplicateURL), errors.Is(err, ErrFolderCycle), errors.Is(err, ErrChildOrder):
		apiJSON(w, http.StatusBadRequest, apiErr(err.Error()))
	default:
		a.serverError(w, r, err)
	}
}

// folderLayers reads the folder id from the path and ?layers=, which is at
// least 1 (the folder's own children) and -1 for every level.
func (a *BookmarksAPI) folderLayers(w http.ResponseWriter, r *http.Request) (int64, int, bool) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return 0, 0, false
	}
	layers := 1
	if s := r.URL.Query().Get("layers"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			apiJSON(w, http.StatusBadRequest, apiErr("invalid layers"))
			return 0, 0, false
		}
		if n < 0 {
			layers = -1
		} else if n > 1 {
			layers = n
		}
	}
	return id, layers, true
}

// queryRootLayers reads a folder id from the query parameter name (the root
// by default) and ?layers=, which defaults to def.
func queryRootLayers(w http.ResponseWriter, r *http.Request, name string, def int) (int64, int, bool) {
	q := r.URL.Query()
	root := RootFolder
	if s := q.Get(name); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			apiJSON(w, http.StatusBadRequest, apiErr("invalid "+name))
			return 0, 0, false
		}
		root = n
	}
	layers := def
	if s := q.Get("layers"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			apiJSON(w, http.StatusBadRequest, apiErr("invalid layers"))
			return 0, 0, false
		}
		layers = n
	}
	return root, layers, true
}

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	n, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil {
		apiJSON(w, http.StatusBadRequest, apiErr("invalid "+name))
		return 0, false
	}
	return n, true
}

// parentPtr is an API parent_folder as a parent id: nil for the root, which
// clients send as -1.
func parentPtr(p *flexID) *int64 {
	if p == nil || int64(*p) == RootFolder {
		return nil
	}
	id := int64(*p)
	return &id
}

// flexID is an id sent as a JSON number or a string of one. Floccus sends
// both: its ids come back from the server as numbers, but it keeps bookmark
// ids as "id;folder" strings and sends the id part as a string.
type flexID int64

func (f *flexID) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		n = json.Number(s)
	}
	id, err := n.Int64()
	if err != nil {
		return fmt.Errorf("id %s: %w", b, err)
	}
	*f = flexID(id)
	return nil
}

// flexIDs is a list of flexID, which stays nil when left out.
type flexIDs []flexID

func (ids flexIDs) int64s() []int64 {
	if ids == nil {
		return nil
	}
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = int64(id)
	}
	return out
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
	parentFolder := RootFolder
	if f.ParentID != nil {
		parentFolder = *f.ParentID
	}
	return map[string]any{
		"id":            f.ID,
		"title":         f.Title,
		"parent_folder": parentFolder,
	}
}

// subfoldersToAPI lists the folders in folder, each with its own subfolders
// while layers (-1 for all) allows.
func subfoldersToAPI(t *Tree, folder int64, layers int) []any {
	out := []any{}
	for _, c := range t.Children(folder) {
		if c.Type != ChildFolder {
			continue
		}
		item := folderToAPI(t.Folder(c.ID))
		if layers != 0 && layers != 1 {
			item["children"] = subfoldersToAPI(t, c.ID, layers-1)
		}
		out = append(out, item)
	}
	return out
}

// childToAPI is one child of a folder as the children endpoint lists it. A
// folder carries its children when layers (-1 for all) is not used up.
func childToAPI(t *Tree, c ChildRef, layers int) map[string]any {
	if c.Type == ChildBookmark {
		item := bookmarkToAPI(t.Bookmark(c.ID))
		item["type"] = ChildBookmark
		return item
	}
	item := folderToAPI(t.Folder(c.ID))
	item["type"] = ChildFolder
	if layers != 0 && t.Loaded(c.ID) {
		cs := t.Children(c.ID)
		children := make([]any, len(cs))
		for i, cc := range cs {
			children[i] = childToAPI(t, cc, layers-1)
		}
		item["children"] = children
	}
	return item
}

func childOrderToAPI(t *Tree, folder int64, layers int) []any {
	cs := t.Children(folder)
	out := make([]any, len(cs))
	for i, c := range cs {
		item := map[string]any{"type": c.Type, "id": c.ID}
		if c.Type == ChildFolder && layers != 1 && t.Loaded(c.ID) {
			item["children"] = childOrderToAPI(t, c.ID, layers-1)
		}
		out[i] = item
	}
	return out
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
