package files

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// API provides HTTP handlers for file operations.
type API struct {
	files  *Plugin
	shares *share.Store
	users  *user.Store
}

// NewAPI creates a new files API handler.
func NewAPI(files *Plugin, shares *share.Store, users *user.Store) *API {
	return &API{files: files, shares: shares, users: users}
}

// RegisterRoutes mounts the files API on the given mux.
func (a *API) RegisterRoutes(mux *http.ServeMux) {
	// File operations
	mux.Handle("GET /api/files/list", auth.RequireAuth(http.HandlerFunc(a.handleList)))
	mux.Handle("POST /api/files/upload", auth.RequireAuth(http.HandlerFunc(a.handleUpload)))
	mux.Handle("GET /api/files/download", auth.RequireAuth(http.HandlerFunc(a.handleDownload)))
	mux.Handle("POST /api/files/mkdir", auth.RequireAuth(http.HandlerFunc(a.handleMkdir)))
	mux.Handle("DELETE /api/files/delete", auth.RequireAuth(http.HandlerFunc(a.handleDelete)))
	mux.Handle("POST /api/files/rename", auth.RequireAuth(http.HandlerFunc(a.handleRename)))
	mux.Handle("GET /api/files/search", auth.RequireAuth(http.HandlerFunc(a.handleSearch)))
	mux.Handle("GET /api/files/info", auth.RequireAuth(http.HandlerFunc(a.handleInfo)))
	mux.Handle("POST /api/files/favorite", auth.RequireAuth(http.HandlerFunc(a.handleFavorite)))
	mux.Handle("GET /api/files/favorites", auth.RequireAuth(http.HandlerFunc(a.handleFavorites)))

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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	files, err := a.files.List(u.Username, path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := a.files.Upload(u.Username, path, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusCreated, meta)
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
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.Name))
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	meta, err := a.files.CreateDir(u.Username, req.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusCreated, meta)
}

func (a *API) handleDelete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path required", http.StatusBadRequest)
		return
	}

	if err := a.files.Delete(u.Username, path); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusOK, meta)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusOK, results)
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

	a.writeJSON(w, http.StatusOK, meta)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleFavorites(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	files, err := a.files.Favorites(u.Username)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if files == nil {
		files = []FileMeta{}
	}

	a.writeJSON(w, http.StatusOK, files)
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if comments == nil {
		comments = []Comment{}
	}

	a.writeJSON(w, http.StatusOK, comments)
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
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusCreated, comment)
}

func (a *API) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	commentIDStr := r.PathValue("id")
	commentID, err := strconv.ParseInt(commentIDStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid comment id", http.StatusBadRequest)
		return
	}

	if err := a.files.DeleteComment(u.Username, commentID, u.Username); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
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
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	rec, err := a.shares.Create(
		u.ID, req.ResourceType, req.ResourceID,
		req.SubjectType, req.SubjectID, req.Permissions,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusCreated, rec)
}

func (a *API) handleShareList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	records, err := seq.Collect(a.shares.ListByOwner(u.ID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []share.Record{}
	}

	a.writeJSON(w, http.StatusOK, records)
}

func (a *API) handleSharesWithMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	records, err := seq.Collect(a.shares.ListBySubject(u.ID))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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

	var enriched []enrichedShare
	for _, rec := range records {
		es := enrichedShare{Record: rec}

		owner, err := a.users.GetByID(rec.OwnerUserID)
		if err == nil {
			es.OwnerUsername = owner.Username
		}

		// If it's a files resource, fetch metadata
		if rec.ResourceType == "files:file" || rec.ResourceType == "files:directory" {
			fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
			if err == nil {
				if owner != nil {
					meta, err := a.files.GetByID(owner.Username, fileID)
					if err == nil {
						es.File = meta
					}
				}
			}
		}

		enriched = append(enriched, es)
	}

	a.writeJSON(w, http.StatusOK, enriched)
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
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if rec.OwnerUserID != u.ID {
		http.Error(w, "not your share", http.StatusForbidden)
		return
	}

	if err := a.shares.Delete(shareID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	rec, err := a.shares.CreatePublicLinkWithPassword(
		u.ID, req.ResourceType, req.ResourceID,
		req.Permissions, req.Expiration, req.Password,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	a.writeJSON(w, http.StatusCreated, rec)
}

func (a *API) handlePublicLinkAccess(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	if token == "" {
		http.Error(w, "token required", http.StatusBadRequest)
		return
	}

	rec, err := a.shares.GetByToken(token)
	if err != nil {
		http.Error(w, "link not found or expired", http.StatusNotFound)
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
			http.Error(w, "owner not found", http.StatusInternalServerError)
			return
		}

		fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
		if err != nil {
			http.Error(w, "invalid resource", http.StatusInternalServerError)
			return
		}

		meta, err := a.files.GetByID(owner.Username, fileID)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}

		a.writeJSON(w, http.StatusOK, map[string]any{
			"file":         meta,
			"permissions":  share.PermissionNames(rec.Permissions),
			"download_url": fmt.Sprintf("/s/%s/download", token),
		})
		return
	}

	a.writeJSON(w, http.StatusOK, map[string]any{
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
		http.Error(w, "link not found or expired", http.StatusNotFound)
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
		http.Error(w, "owner not found", http.StatusInternalServerError)
		return
	}

	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		http.Error(w, "invalid resource", http.StatusInternalServerError)
		return
	}

	meta, err := a.files.GetByID(owner.Username, fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	f, _, err := a.files.Download(owner.Username, meta.Path)
	if err != nil {
		http.Error(w, "download failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.Name))
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
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}

	// Check access
	decision, err := a.shares.CheckAccess(u.ID, rec.ResourceType, rec.ResourceID)
	if err != nil || !decision.Allowed {
		http.Error(w, "access denied", http.StatusForbidden)
		return
	}

	if rec.ResourceType != "files:file" {
		http.Error(w, "not a file resource", http.StatusBadRequest)
		return
	}

	owner, err := a.users.GetByID(rec.OwnerUserID)
	if err != nil {
		http.Error(w, "owner not found", http.StatusInternalServerError)
		return
	}

	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err != nil {
		http.Error(w, "invalid resource", http.StatusInternalServerError)
		return
	}

	meta, err := a.files.GetByID(owner.Username, fileID)
	if err != nil {
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}

	f, _, err := a.files.Download(owner.Username, meta.Path)
	if err != nil {
		http.Error(w, "download failed", http.StatusInternalServerError)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", meta.MimeType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, meta.Name))
	w.Header().Set("ETag", meta.ETag)
	if _, err := io.Copy(w, f); err != nil {
		a.files.log.Error("shared download copy", "err", err)
	}
}

func (a *API) verifyOwnership(u *user.User, resourceType, resourceID string) error {
	if resourceType != "files:file" && resourceType != "files:directory" {
		return fmt.Errorf("unsupported resource type: %s", resourceType)
	}

	fileID, err := strconv.ParseInt(resourceID, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid resource id: %s", resourceID)
	}

	_, err = a.files.GetByID(u.Username, fileID)
	if err != nil {
		return fmt.Errorf("resource not found or not owned")
	}

	return nil
}

// checkSharePassword extracts a password from the request (Basic auth or query param)
// and validates it against the share record.
func (a *API) checkSharePassword(rec *share.Record, r *http.Request) error {
	if rec.PasswordHash == nil || *rec.PasswordHash == "" {
		return nil
	}
	// Try Basic auth password first (username is ignored for share links).
	if _, password, ok := r.BasicAuth(); ok {
		return share.CheckPassword(rec, password)
	}
	// Fall back to query parameter.
	password := r.URL.Query().Get("password")
	return share.CheckPassword(rec, password)
}

func (a *API) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		a.files.log.Error("json encode", "err", err)
	}
}
