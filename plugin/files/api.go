package files

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"io"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/bodylimit"
	"git.duckfam.us/jj-software/cog/internal/httphdr"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// API provides HTTP handlers for file operations.
type API struct {
	files  *Plugin
	shares *share.Store
	users  *user.Store
	// guard throttles guessing at public links' passwords, as at accounts'
	// (audit security.md #36).
	guard *ratelimit.Guard
}

// NewAPI creates a new files API handler.
func NewAPI(files *Plugin, shares *share.Store, users *user.Store, guard *ratelimit.Guard) *API {
	return &API{files: files, shares: shares, users: users, guard: guard}
}

// RegisterRoutes mounts the files API on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	// File operations
	mux.Handle("GET /api/files/list", auth.RequireAuth(http.HandlerFunc(a.handleList)))
	mux.Handle("POST /api/files/upload", bodylimit.Unlimited(auth.RequireAuth(http.HandlerFunc(a.handleUpload))))
	mux.Handle("GET /api/files/download", auth.RequireAuth(http.HandlerFunc(a.handleDownload)))
	mux.Handle("POST /api/files/mkdir", auth.RequireAuth(http.HandlerFunc(a.handleMkdir)))
	mux.Handle("DELETE /api/files/delete", auth.RequireAuth(http.HandlerFunc(a.handleDelete)))
	mux.Handle("POST /api/files/rename", auth.RequireAuth(http.HandlerFunc(a.handleRename)))
	mux.Handle("GET /api/files/search", auth.RequireAuth(http.HandlerFunc(a.handleSearch)))
	mux.Handle("GET /api/files/info", auth.RequireAuth(http.HandlerFunc(a.handleInfo)))
	mux.Handle("POST /api/files/favorite", auth.RequireAuth(http.HandlerFunc(a.handleFavorite)))
	mux.Handle("GET /api/files/favorites", auth.RequireAuth(http.HandlerFunc(a.handleFavorites)))

	// Comments
	mux.Handle("GET /api/files/comments", auth.RequireAuth(http.HandlerFunc(a.handleListComments)))
	mux.Handle("POST /api/files/comments", auth.RequireAuth(http.HandlerFunc(a.handleCreateComment)))
	mux.Handle("DELETE /api/files/comments/{id}", auth.RequireAuth(http.HandlerFunc(a.handleDeleteComment)))

	// Sharing operations
	mux.Handle("POST /api/shares", auth.RequireAuth(http.HandlerFunc(a.handleShareCreate)))
	mux.Handle("GET /api/shares", auth.RequireAuth(http.HandlerFunc(a.handleShareList)))
	mux.Handle("GET /api/shares/with-me", auth.RequireAuth(http.HandlerFunc(a.handleSharesWithMe)))
	mux.Handle("DELETE /api/shares/{id}", auth.RequireAuth(http.HandlerFunc(a.handleShareDelete)))
	mux.Handle("POST /api/shares/public-link", auth.RequireAuth(http.HandlerFunc(a.handlePublicLinkCreate)))

	// Public link access (no auth required)
	mux.HandleFunc("GET /s/{token}", a.handlePublicLinkAccess)
	mux.HandleFunc("GET /s/{token}/download", a.handlePublicLinkDownload)

	// Shared file download (auth required)
	mux.Handle("GET /api/shares/{id}/download", auth.RequireAuth(http.HandlerFunc(a.handleSharedDownload)))
}

func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		path = "/"
	}

	if err := a.files.EnsureRootDir(u.Username); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	files, err := a.files.List(u.Username, path)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"path":  path,
		"files": files,
	})
}

func (a *API) handleUpload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := a.files.EnsureRootDir(u.Username); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	meta, err := a.files.Upload(u.Username, path, r.Body)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusCreated, meta)
}

func (a *API) handleDownload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	f, meta, err := a.files.Download(u.Username, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.MimeType)
	plugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("Content-Disposition", httphdr.Attachment(meta.Name))
	w.Header().Set("ETag", meta.ETag)
	if _, err := io.Copy(w, f); err != nil {
		a.files.log.Error("download copy", "err", err)
	}
}

func (a *API) handleMkdir(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := a.files.EnsureRootDir(u.Username); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	meta, err := a.files.CreateDir(u.Username, req.Path)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusCreated, meta)
}

func (a *API) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := a.files.Delete(u.Username, path); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleRename(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		OldPath string `json:"old_path"`
		NewPath string `json:"new_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.OldPath == "" || req.NewPath == "" {
		http.Error(w, "old_path and new_path required", http.StatusBadRequest)
		return
	}

	meta, err := a.files.Rename(u.Username, req.OldPath, req.NewPath)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, meta)
}

func (a *API) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "q parameter required", http.StatusBadRequest)
		return
	}

	results, err := a.files.Search(u.Username, query)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, results)
}

func (a *API) handleInfo(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	meta, err := a.files.GetByPath(u.Username, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	plugin.WriteJSON(w, http.StatusOK, meta)
}

func (a *API) handleFavorite(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		Path     string `json:"path"`
		Favorite bool   `json:"favorite"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := a.files.SetFavorite(u.Username, req.Path, req.Favorite); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleFavorites(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	files, err := a.files.Favorites(u.Username)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if files == nil {
		files = []FileMeta{}
	}

	plugin.WriteJSON(w, http.StatusOK, files)
}

// --- Comment handlers ---

func (a *API) handleListComments(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	fileIDStr := r.URL.Query().Get("file_id")
	fileID, err := strconv.ParseInt(fileIDStr, 10, 64)
	if err != nil {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}

	comments, err := a.files.ListComments(u.Username, fileID)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if comments == nil {
		comments = []Comment{}
	}

	plugin.WriteJSON(w, http.StatusOK, comments)
}

func (a *API) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		FileID  int64  `json:"file_id"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.FileID == 0 || req.Message == "" {
		http.Error(w, "file_id and message required", http.StatusBadRequest)
		return
	}

	comment, err := a.files.CreateComment(u.Username, req.FileID, u.Username, req.Message)
	if errors.Is(err, ErrCommentTooLong) {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		a.serverError(w, "create comment", err)
		return
	}

	plugin.WriteJSON(w, http.StatusCreated, comment)
}

func (a *API) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	commentIDStr := r.PathValue("id")
	commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid comment id", http.StatusBadRequest)
		return
	}

	if err := a.files.DeleteComment(u.Username, commentID, u.Username); errors.Is(err, ErrNotFound) {
		http.Error(w, "comment not found", http.StatusNotFound)
		return
	} else if err != nil {
		a.files.log.Error("delete comment", "user", u.Username, "comment", commentID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Sharing handlers ---

func (a *API) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		ResourceType string `json:"resource_type"` // e.g. "files:file"
		ResourceID   string `json:"resource_id"`   // file ID
		SubjectType  string `json:"subject_type"`  // "USER"
		SubjectID    string `json:"subject_id"`    // user ID
		Permissions  uint32 `json:"permissions"`   // bitmask
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.ResourceType == "" || req.ResourceID == "" {
		http.Error(w, "resource_type and resource_id required", http.StatusBadRequest)
		return
	}
	if req.SubjectType == "" || req.SubjectID == "" {
		http.Error(w, "subject_type and subject_id required", http.StatusBadRequest)
		return
	}
	if req.Permissions == 0 {
		req.Permissions = plugin.PermRead
	}

	// Verify the resource exists and belongs to this user (for files)
	if err := a.verifyOwnership(u, req.ResourceType, req.ResourceID); err != nil {
		if errors.Is(err, errNotOwned) {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			a.serverError(w, "verify ownership", err)
		}
		return
	}

	rec, err := a.shares.Create(
		u.ID, req.ResourceType, req.ResourceID,
		req.SubjectType, req.SubjectID, req.Permissions,
	)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusCreated, rec)
}

func (a *API) handleShareList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	records, err := seq.Collect(a.shares.ListByOwner(u.ID))
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if records == nil {
		records = []share.Record{}
	}

	plugin.WriteJSON(w, http.StatusOK, records)
}

func (a *API) handleSharesWithMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	records, err := seq.Collect(a.shares.ListBySubject(u.ID))
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	if records == nil {
		records = []share.Record{}
	}

	// Enrich with owner info and file metadata
	type enrichedShare struct {
		share.Record
		OwnerUsername string    `json:"owner_username"`
		File          *FileMeta `json:"file,omitempty"`
	}

	// One query for the owners, and one per owner for the files shared: a
	// page of shares from the same person is the usual case.
	ownerIDs := make([]string, len(records))
	for i, rec := range records {
		ownerIDs[i] = rec.OwnerUserID
	}
	owners, err := a.users.GetByIDs(ownerIDs)
	if err != nil {
		a.files.log.Error("shares with me: owners", "err", err)
		http.Error(w, "failed to load shares", http.StatusInternalServerError)
		return
	}
	enriched := make([]enrichedShare, len(records))
	fileIDs := map[string][]int64{} // owner username -> shared file IDs
	for i, rec := range records {
		enriched[i] = enrichedShare{Record: rec}
		owner := owners[rec.OwnerUserID]
		if owner == nil {
			continue // the owner has since been deleted
		}
		enriched[i].OwnerUsername = owner.Username
		if rec.ResourceType != "files:file" && rec.ResourceType != "files:directory" {
			continue
		}
		fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			a.files.log.Error("shares with me: bad file id", "share", rec.ID, "resource", rec.ResourceID, "err", err)
			continue
		}
		fileIDs[owner.Username] = append(fileIDs[owner.Username], fileID)
	}

	metas := map[string]map[int64]*FileMeta{}
	for username, ids := range fileIDs {
		db, err := a.files.DB(username)
		if err == nil {
			metas[username], err = getByIDs(db, ids)
		}
		if err != nil {
			a.files.log.Error("shares with me: files", "owner", username, "err", err)
			http.Error(w, "failed to load shares", http.StatusInternalServerError)
			return
		}
	}
	for i := range enriched {
		es := &enriched[i]
		if es.ResourceType != "files:file" && es.ResourceType != "files:directory" {
			continue
		}
		if fileID, err := strconv.ParseInt(es.ResourceID, 10, 64); err == nil {
			es.File = metas[es.OwnerUsername][fileID] // nil if the file has gone
		}
	}

	plugin.WriteJSON(w, http.StatusOK, enriched)
}

func (a *API) handleShareDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	shareID := r.PathValue("id")
	if shareID == "" {
		http.Error(w, "share id required", http.StatusBadRequest)
		return
	}

	// Verify ownership
	rec, err := a.shares.Get(shareID)
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			http.Error(w, "share not found", http.StatusNotFound)
		} else {
			a.serverError(w, "get share", err)
		}
		return
	}
	if rec.OwnerUserID != u.ID {
		http.Error(w, "not your share", http.StatusForbidden)
		return
	}

	if err := a.shares.Delete(shareID); err != nil {
		httperr.Internal(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handlePublicLinkCreate(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	var req struct {
		ResourceType string `json:"resource_type"`
		ResourceID   string `json:"resource_id"`
		Permissions  uint32 `json:"permissions"`
		Expiration   *int64 `json:"expiration"` // unix timestamp
		Password     string `json:"password"`   // optional password protection
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.ResourceType == "" || req.ResourceID == "" {
		http.Error(w, "resource_type and resource_id required", http.StatusBadRequest)
		return
	}
	if req.Permissions == 0 {
		req.Permissions = plugin.PermRead
	}

	if err := a.verifyOwnership(u, req.ResourceType, req.ResourceID); err != nil {
		if errors.Is(err, errNotOwned) {
			http.Error(w, err.Error(), http.StatusForbidden)
		} else {
			a.serverError(w, "verify ownership", err)
		}
		return
	}

	rec, err := a.shares.CreatePublicLinkWithPassword(
		u.ID, req.ResourceType, req.ResourceID,
		req.Permissions, req.Expiration, req.Password,
	)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}

	plugin.WriteJSON(w, http.StatusCreated, rec)
}

func (a *API) handlePublicLinkAccess(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}

	rec, err := a.shares.GetByToken(token)
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			http.Error(w, "link not found or expired", http.StatusNotFound)
		} else {
			a.serverError(w, "public link", err)
		}
		return
	}

	// Check password if set.
	if err := a.checkSharePassword(rec, r); err != nil {
		if err == share.ErrPasswordRequired {
			w.Header().Set("WWW-Authenticate", `Basic realm="Password required"`)
			http.Error(w, "password required", http.StatusUnauthorized)
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="Password required"`)
			http.Error(w, "incorrect password", http.StatusUnauthorized)
		}
		return
	}

	// Resolve file metadata
	if rec.ResourceType == "files:file" || rec.ResourceType == "files:directory" {
		owner, err := a.users.GetByID(rec.OwnerUserID)
		if err != nil {
			a.serverError(w, "share owner", err)
			return
		}

		fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			http.Error(w, "invalid resource", http.StatusInternalServerError)
			return
		}

		meta, err := a.files.GetByID(owner.Username, fileID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				http.Error(w, "file not found", http.StatusNotFound)
			} else {
				a.serverError(w, "shared file", err)
			}
			return
		}

		plugin.WriteJSON(w, http.StatusOK, map[string]any{
			"file":         meta,
			"permissions":  share.PermissionNames(rec.Permissions),
			"download_url": fmt.Sprintf("/s/%s/download", token),
		})
		return
	}

	plugin.WriteJSON(w, http.StatusOK, map[string]any{
		"resource_type": rec.ResourceType,
		"resource_id":   rec.ResourceID,
		"permissions":   share.PermissionNames(rec.Permissions),
	})
}

func (a *API) handlePublicLinkDownload(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}

	rec, err := a.shares.GetByToken(token)
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			http.Error(w, "link not found or expired", http.StatusNotFound)
		} else {
			a.serverError(w, "public link", err)
		}
		return
	}

	// Check password if set.
	if err := a.checkSharePassword(rec, r); err != nil {
		if err == share.ErrPasswordRequired {
			w.Header().Set("WWW-Authenticate", `Basic realm="Password required"`)
			http.Error(w, "password required", http.StatusUnauthorized)
		} else {
			w.Header().Set("WWW-Authenticate", `Basic realm="Password required"`)
			http.Error(w, "incorrect password", http.StatusUnauthorized)
		}
		return
	}

	if rec.Permissions&plugin.PermRead == 0 {
		http.Error(w, "no read permission", http.StatusForbidden)
		return
	}

	if rec.ResourceType != "files:file" {
		http.Error(w, "not a file resource", http.StatusBadRequest)
		return
	}

	owner, err := a.users.GetByID(rec.OwnerUserID)
	if err != nil {
		a.serverError(w, "share owner", err)
		return
	}

	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		http.Error(w, "invalid resource", http.StatusInternalServerError)
		return
	}

	meta, err := a.files.GetByID(owner.Username, fileID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			a.serverError(w, "shared file", err)
		}
		return
	}

	f, _, err := a.files.Download(owner.Username, meta.Path)
	if err != nil {
		a.serverError(w, "shared download", err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.MimeType)
	plugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("Content-Disposition", httphdr.Attachment(meta.Name))
	w.Header().Set("ETag", meta.ETag)
	if _, err := io.Copy(w, f); err != nil {
		a.files.log.Error("public link download copy", "err", err)
	}
}

func (a *API) handleSharedDownload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	shareID := r.PathValue("id")

	rec, err := a.shares.Get(shareID)
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			http.Error(w, "share not found", http.StatusNotFound)
		} else {
			a.serverError(w, "get share", err)
		}
		return
	}

	// The file is looked up in the share owner's files, so only that
	// owner's shares of it count: a share of the same ID someone else made
	// grants nothing here.
	decision, err := a.shares.CheckAccessGrantedBy(u.ID, rec.ResourceType, rec.ResourceID,
		func(ownerID string) (bool, error) { return ownerID == rec.OwnerUserID, nil })
	if err != nil {
		a.files.log.Error("shared download: check access", "share", shareID, "err", err)
		http.Error(w, "failed to check access", http.StatusInternalServerError)
		return
	}
	if !decision.Allowed {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	if rec.ResourceType != "files:file" {
		http.Error(w, "not a file resource", http.StatusBadRequest)
		return
	}

	owner, err := a.users.GetByID(rec.OwnerUserID)
	if err != nil {
		a.serverError(w, "share owner", err)
		return
	}

	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		http.Error(w, "invalid resource", http.StatusInternalServerError)
		return
	}

	meta, err := a.files.GetByID(owner.Username, fileID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "file not found", http.StatusNotFound)
		} else {
			a.serverError(w, "shared file", err)
		}
		return
	}

	f, _, err := a.files.Download(owner.Username, meta.Path)
	if err != nil {
		a.serverError(w, "shared download", err)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.MimeType)
	plugin.UntrustedContent(w, meta.MimeType)
	w.Header().Set("Content-Disposition", httphdr.Attachment(meta.Name))
	w.Header().Set("ETag", meta.ETag)
	if _, err := io.Copy(w, f); err != nil {
		a.files.log.Error("shared download copy", "err", err)
	}
}

// errNotOwned is a share request for something the user cannot share.
var errNotOwned = errors.New("forbidden")

// serverError logs err and answers 500 without exposing it.
func (a *API) serverError(w http.ResponseWriter, what string, err error) {
	a.files.log.Error(what, "err", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func (a *API) verifyOwnership(u *user.User, resourceType, resourceID string) error {
	if resourceType != "files:file" && resourceType != "files:directory" {
		return fmt.Errorf("%w: unsupported resource type: %s", errNotOwned, resourceType)
	}

	fileID, err := strconv.ParseInt(resourceID, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: invalid resource id: %s", errNotOwned, resourceID)
	}

	meta, err := a.files.GetByID(u.Username, fileID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: resource not found or not owned", errNotOwned)
		}
		return err
	}
	if err := a.files.MayShare(u.Username, meta.Path); errors.Is(err, ErrNoReshare) {
		return fmt.Errorf("%w: %w", errNotOwned, err)
	} else if err != nil {
		return err
	}
	return nil
}

// checkSharePassword extracts a password from the request (Basic auth or query param)
// and validates it against the share record, through the guard: the link is
// the account, and a client that guesses too often is refused.
func (a *API) checkSharePassword(rec *share.Record, r *http.Request) error {
	if rec.PasswordHash == nil || *rec.PasswordHash == "" {
		return nil
	}

	// The password travels as Basic credentials, never in the URL, where it
	// would land in access logs and browser history. A client still sending
	// it there is logged, so a client that stops working is explained.
	if r.URL.Query().Has("password") {
		a.files.log.Warn("share password sent in URL query is ignored; send it as Basic auth",
			"share", rec.ID, "ip", ratelimit.ClientIP(r))
	}
	_, password, _ := r.BasicAuth()

	// Only ok says the password was right: a repeat of a refused one is
	// refused without check running, and checkErr is then still nil.
	var checkErr error
	ok, err := a.guard.Try(ratelimit.ClientIP(r), ratelimit.ShareKey(rec.ID), password, nil, func() (bool, error) {
		checkErr = share.CheckPassword(rec, password)
		return checkErr == nil, nil
	})
	switch {
	case ok:
		return nil
	case err == nil && checkErr != nil:
		return checkErr
	default:
		return share.ErrPasswordIncorrect
	}
}
