package compat

import (
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"git.duckfam.us/jj-software/cog/internal/activity"
	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// NCCompat holds dependencies for NextCloud-compatible OCS/API endpoints.
type NCCompat struct {
	Users      *user.Store
	Files      *files.Plugin
	Activities *activity.Store
	// OpenAvatar opens a stored avatar image by the name users.avatar holds.
	OpenAvatar func(name string) (io.ReadCloser, error)
	// EmailVisible says whose addresses a user may see.
	EmailVisible EmailVisible
}

// RegisterRoutes registers all NextCloud-compatible OCS and helper routes.
func (nc *NCCompat) RegisterRoutes(mux *http.ServeMux) {
	// Connectivity check (NC clients use GET /index.php/204 to test)
	mux.HandleFunc("GET /index.php/204", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Avatar (serves real uploaded avatar or initials SVG). Signed in only:
	// the answer says whether the name is an account.
	mux.Handle("GET /index.php/avatar/{userID}/{size}", auth.RequireAuth(http.HandlerFunc(nc.HandleAvatar)))

	// Notifications (no notification system yet — empty list is correct)
	handleNotifications := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/notifications/api/v2/notifications", handleNotifications)
	mux.HandleFunc("GET /ocs/v1.php/apps/notifications/api/v2/notifications", handleNotifications)

	// Recommendations (no recommendation engine — empty list is correct)
	handleRecommendations := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/recommendations/api/v1/recommendations", handleRecommendations)
	mux.HandleFunc("GET /ocs/v1.php/apps/recommendations/api/v1/recommendations", handleRecommendations)

	// User status (returns authenticated user's status)
	mux.Handle("GET /ocs/v2.php/apps/user_status/api/v1/user_status", auth.RequireAuth(http.HandlerFunc(nc.HandleUserStatus)))
	mux.Handle("GET /ocs/v1.php/apps/user_status/api/v1/user_status", auth.RequireAuth(http.HandlerFunc(nc.HandleUserStatus)))

	// Predefined user statuses (NC Android app fetches this for the status picker)
	handlePredefinedStatuses := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{
			map[string]any{"id": "meeting", "icon": "📅", "message": "In a meeting", "clearAt": nil},
			map[string]any{"id": "commuting", "icon": "🚌", "message": "Commuting", "clearAt": nil},
			map[string]any{"id": "remote-work", "icon": "🏡", "message": "Working remotely", "clearAt": nil},
			map[string]any{"id": "sick-leave", "icon": "🤒", "message": "Out sick", "clearAt": nil},
			map[string]any{"id": "vacationing", "icon": "🌴", "message": "Vacationing", "clearAt": nil},
		})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/user_status/api/v1/statuses/predefined", handlePredefinedStatuses)
	mux.HandleFunc("GET /ocs/v1.php/apps/user_status/api/v1/statuses/predefined", handlePredefinedStatuses)

	// User status heartbeat (presence tracking — accept and ignore)
	handleHeartbeat := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, map[string]any{
			"userId": "", "status": "online", "icon": nil, "message": nil,
			"messageId": nil, "messageIsPredefined": false, "clearAt": nil,
			"statusIsUserDefined": false,
		})
	}
	mux.HandleFunc("POST /ocs/v2.php/apps/user_status/api/v1/heartbeat", handleHeartbeat)
	mux.HandleFunc("POST /ocs/v1.php/apps/user_status/api/v1/heartbeat", handleHeartbeat)

	// User status set (accept PUT to current status — no-op)
	handleSetStatus := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, map[string]any{
			"userId": "", "status": "online", "icon": nil, "message": nil,
			"messageId": nil, "messageIsPredefined": false, "clearAt": nil,
			"statusIsUserDefined": false,
		})
	}
	mux.HandleFunc("PUT /ocs/v2.php/apps/user_status/api/v1/user_status/status", handleSetStatus)
	mux.HandleFunc("PUT /ocs/v1.php/apps/user_status/api/v1/user_status/status", handleSetStatus)
	mux.HandleFunc("PUT /ocs/v2.php/apps/user_status/api/v1/user_status/message/predefined", handleSetStatus)
	mux.HandleFunc("PUT /ocs/v1.php/apps/user_status/api/v1/user_status/message/predefined", handleSetStatus)
	mux.HandleFunc("PUT /ocs/v2.php/apps/user_status/api/v1/user_status/message/custom", handleSetStatus)
	mux.HandleFunc("PUT /ocs/v1.php/apps/user_status/api/v1/user_status/message/custom", handleSetStatus)
	mux.HandleFunc("DELETE /ocs/v2.php/apps/user_status/api/v1/user_status/message", handleSetStatus)
	mux.HandleFunc("DELETE /ocs/v1.php/apps/user_status/api/v1/user_status/message", handleSetStatus)

	// Search providers (files search provider registered)
	handleSearchProviders := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{
			map[string]any{
				"id":          "files",
				"name":        "Files",
				"order":       5,
				"icon":        "",
				"isPaginated": false,
			},
		})
	}
	mux.HandleFunc("GET /ocs/v2.php/search/providers", handleSearchProviders)
	mux.HandleFunc("GET /ocs/v1.php/search/providers", handleSearchProviders)

	// Unified search query
	mux.Handle("GET /ocs/v2.php/search/providers/{providerId}/search", auth.RequireAuth(http.HandlerFunc(nc.HandleSearch)))
	mux.Handle("GET /ocs/v1.php/search/providers/{providerId}/search", auth.RequireAuth(http.HandlerFunc(nc.HandleSearch)))

	// Tags API (NC clients use this for file tagging)
	handleTags := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/files/api/v1/tags", handleTags)
	mux.HandleFunc("GET /ocs/v1.php/apps/files/api/v1/tags", handleTags)

	// Client diagnostics (telemetry — accept and ignore)
	handleDiag := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, []any{})
	}
	mux.HandleFunc("POST /ocs/v2.php/apps/admin_notifications/api/v1/client_diagnostic", handleDiag)
	mux.HandleFunc("POST /ocs/v1.php/apps/admin_notifications/api/v1/client_diagnostic", handleDiag)

	// Profiles / hover cards (cosmetic — return minimal info)
	mux.Handle("GET /ocs/v2.php/apps/dav/api/v1/profiles/{userId}", auth.RequireAuth(http.HandlerFunc(nc.HandleProfile)))
	mux.Handle("GET /ocs/v1.php/apps/dav/api/v1/profiles/{userId}", auth.RequireAuth(http.HandlerFunc(nc.HandleProfile)))

	// File locking OCS endpoints (stub — accept and return success)
	handleLock := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, map[string]any{})
	}
	handleUnlock := func(w http.ResponseWriter, r *http.Request) {
		writeOCSReq(w, r, http.StatusOK, map[string]any{})
	}
	mux.HandleFunc("POST /ocs/v2.php/apps/files_lock/lock/{fileId}", handleLock)
	mux.HandleFunc("POST /ocs/v1.php/apps/files_lock/lock/{fileId}", handleLock)
	mux.HandleFunc("DELETE /ocs/v2.php/apps/files_lock/lock/{fileId}", handleUnlock)
	mux.HandleFunc("DELETE /ocs/v1.php/apps/files_lock/lock/{fileId}", handleUnlock)

	// Activity API — returns comment activities for files.
	mux.Handle("GET /ocs/v2.php/apps/activity/api/v2/activity", auth.RequireAuth(http.HandlerFunc(nc.HandleActivity)))
	mux.Handle("GET /ocs/v1.php/apps/activity/api/v2/activity", auth.RequireAuth(http.HandlerFunc(nc.HandleActivity)))
	mux.Handle("GET /ocs/v2.php/apps/activity/api/v2/activity/filter", auth.RequireAuth(http.HandlerFunc(nc.HandleActivity)))
	mux.Handle("GET /ocs/v1.php/apps/activity/api/v2/activity/filter", auth.RequireAuth(http.HandlerFunc(nc.HandleActivity)))

	// .well-known discovery redirects
	wellKnownDAV := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/remote.php/dav/", http.StatusMovedPermanently)
	}
	mux.HandleFunc("GET /.well-known/caldav", wellKnownDAV)
	mux.HandleFunc("PROPFIND /.well-known/caldav", wellKnownDAV)
	mux.HandleFunc("GET /.well-known/carddav", wellKnownDAV)
	mux.HandleFunc("PROPFIND /.well-known/carddav", wellKnownDAV)
}

// HandleAvatar serves a user's avatar image at the NC-compatible path.
// It serves the uploaded avatar if one exists, otherwise generates an
// initials-based SVG.
func (nc *NCCompat) HandleAvatar(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")

	u, err := userByIDOrName(nc.Users, userID)
	if err != nil {
		if !errors.Is(err, user.ErrNotFound) {
			slog.Error("nc avatar: look up user", "user", userID, "err", err)
		}
		serveInitialsSVG(w, "?", userID)
		return
	}

	if u.Avatar != "" {
		f, err := nc.OpenAvatar(u.Avatar)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			slog.Error("nc avatar: open image", "user", u.ID, "err", err)
		}
		if err == nil {
			defer f.Close()
			ext := filepath.Ext(u.Avatar)
			switch ext {
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			default:
				w.Header().Set("Content-Type", "image/jpeg")
			}
			w.Header().Set("Cache-Control", "private, max-age=3600")
			io.Copy(w, f)
			return
		}
	}

	serveInitialsSVG(w, userInitials(u.Username, u.DisplayName), u.Username)
}

// HandleUserStatus returns the authenticated user's status.
func (nc *NCCompat) HandleUserStatus(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"userId":              u.Username,
		"message":             nil,
		"messageId":           nil,
		"messageIsPredefined": false,
		"icon":                nil,
		"clearAt":             nil,
		"status":              "online",
		"statusIsUserDefined": false,
	})
}

// HandleActivity returns activities from the centralized audit log.
// NC Android app calls: GET /ocs/v2.php/apps/activity/api/v2/activity/filter?object_type=files&object_id={id}&format=json
func (nc *NCCompat) HandleActivity(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	objectType := r.URL.Query().Get("object_type")
	objectIDStr := r.URL.Query().Get("object_id")
	sinceStr := r.URL.Query().Get("since")
	limitStr := r.URL.Query().Get("limit")

	limit := 50
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}

	var entries []activity.Activity
	var err error

	if nc.Activities != nil {
		if objectType != "" && objectIDStr != "" {
			entries, err = nc.Activities.ListByObject(objectType, objectIDStr, limit)
		} else if sinceStr != "" {
			sinceID, perr := strconv.ParseInt(sinceStr, 10, 64)
			if perr != nil {
				writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid since")
				return
			}
			entries, err = nc.Activities.ListSince(u.Username, sinceID, limit)
		} else {
			entries, err = nc.Activities.List(u.Username, limit, 0)
		}
	}
	if err != nil {
		slog.Error("nc activity: list", "user", u.Username, "err", err)
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "list activities failed")
		return
	}

	activities := make([]map[string]any, 0, len(entries))
	for _, a := range entries {
		// Build subject_rich from template + params if available.
		var subjectRich []any
		if a.SubjectTemplate != "" {
			var params map[string]any
			if err := json.Unmarshal([]byte(a.SubjectParams), &params); err != nil {
				// Fall back to the plain subject below.
				slog.Warn("nc activity: bad subject params", "activity", a.ID, "err", err)
			} else {
				subjectRich = []any{a.SubjectTemplate, params}
			}
		}
		if subjectRich == nil {
			subjectRich = []any{a.Subject, map[string]any{}}
		}

		activities = append(activities, map[string]any{
			"activity_id":  a.ID,
			"app":          a.App,
			"type":         a.Type,
			"affecteduser": u.Username,
			"user":         a.ActorID,
			"subject":      a.Subject,
			"subject_rich": subjectRich,
			"message":      "",
			"message_rich": []any{"", map[string]any{}},
			"object_type":  a.ObjectType,
			"object_id":    a.ObjectID,
			"object_name":  a.ObjectName,
			"datetime":     time.Unix(a.CreatedAt, 0).UTC().Format(time.RFC3339),
			"icon":         "",
			"link":         "",
			"previews":     []any{},
		})
	}

	writeOCSReq(w, r, http.StatusOK, activities)
}

// PreviewHandler serves NextCloud-compatible preview thumbnails.
// Route: GET /index.php/core/preview?fileId=N&x=W&y=H
type PreviewHandler struct {
	Files *files.Plugin
}

func (h *PreviewHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	fileIDStr := r.URL.Query().Get("fileId")
	fileID, err := strconv.ParseInt(fileIDStr, 10, 64)
	if err != nil || fileID <= 0 {
		// NC clients may send hex-encoded file IDs (from oc:id in PROPFIND)
		fileID, err = strconv.ParseInt(fileIDStr, 16, 64)
		if err != nil || fileID <= 0 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}

	meta, err := h.Files.GetByID(u.Username, fileID)
	if errors.Is(err, files.ErrNotFound) {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("nc preview: look up file", "user", u.Username, "file", fileID, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if !files.HasPreview(meta.MimeType) {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	x, _ := strconv.Atoi(r.URL.Query().Get("x"))
	y, _ := strconv.Atoi(r.URL.Query().Get("y"))
	size := max(y, x)
	if size <= 0 {
		size = 256
	}

	f, err := h.Files.Preview(u.Username, meta.Path, size)
	if err != nil {
		// A file the thumbnailer can't read has no preview; clients fall
		// back to an icon on 404.
		slog.Warn("nc preview", "user", u.Username, "path", meta.Path, "err", err)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	io.Copy(w, f)
}

// HandleSearch implements the unified search query endpoint.
// NC Android app calls: GET /ocs/v2.php/search/providers/files/search?term=query&from=0&limit=25
func (nc *NCCompat) HandleSearch(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	providerID := r.PathValue("providerId")
	term := r.URL.Query().Get("term")

	if providerID != "files" || nc.Files == nil || term == "" {
		writeOCSReq(w, r, http.StatusOK, map[string]any{
			"name":    providerID,
			"entries": []any{},
			"cursor":  0,
		})
		return
	}

	results, err := nc.Files.Search(u.Username, term)
	if err != nil {
		slog.Error("nc search", "user", u.Username, "err", err)
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "search failed")
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 25
	if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
		limit = n
	}
	if len(results) > limit {
		results = results[:limit]
	}

	entries := make([]map[string]any, 0, len(results))
	for _, meta := range results {
		icon := "file"
		if meta.IsDir {
			icon = "folder"
		}
		entries = append(entries, map[string]any{
			"thumbnailUrl": "",
			"title":        meta.Name,
			"subline":      meta.Path,
			"resourceUrl":  "/remote.php/dav/files/" + u.Username + meta.Path,
			"icon":         icon,
			"rounded":      false,
			"attributes": map[string]any{
				"fileId": strconv.FormatInt(meta.ID, 10),
				"path":   meta.Path,
			},
		})
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"name":    "Files",
		"entries": entries,
		"cursor":  0,
	})
}

// HandleProfile returns minimal profile data for hover cards.
func (nc *NCCompat) HandleProfile(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userId")

	target, err := userByIDOrName(nc.Users, userID)
	if errors.Is(err, user.ErrNotFound) {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "user not found")
		return
	}
	if err != nil {
		slog.Error("nc profile: look up user", "user", userID, "err", err)
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "internal error")
		return
	}

	displayName := target.DisplayName
	if displayName == "" {
		displayName = target.Username
	}

	// The address is shown to those who would see it on the user's OCS
	// record: the user, admins, and people in a group with them.
	email := ""
	if nc.EmailVisible != nil {
		ok, err := nc.EmailVisible(auth.UserFromContext(r.Context()), target.ID)
		if err != nil {
			slog.Error("nc profile: email visibility", "user", target.Username, "err", err)
		}
		if ok {
			email = target.Email
		}
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"userId":      target.Username,
		"displayName": displayName,
		"address":     "",
		"website":     "",
		"twitter":     "",
		"phone":       "",
		"email":       email,
		"actions":     []any{},
	})
}

// --- Avatar helpers ---

func serveInitialsSVG(w http.ResponseWriter, init string, seed string) {
	colors := []string{
		"#e53935", "#8e24aa", "#3949ab", "#039be5",
		"#00897b", "#43a047", "#f4511e", "#6d4c41",
		"#546e7a", "#d81b60", "#5e35b1", "#1e88e5",
	}
	hash := md5.Sum([]byte(seed))
	color := colors[int(hash[0])%len(colors)]

	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <circle cx="32" cy="32" r="32" fill="%s"/>
  <text x="32" y="32" dy=".35em" text-anchor="middle" fill="white" font-family="sans-serif" font-size="24" font-weight="600">%s</text>
</svg>`, color, html.EscapeString(init))
}

func userInitials(username, displayName string) string {
	name := displayName
	if name == "" {
		name = username
	}
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	if len(parts) == 1 {
		for _, r := range parts[0] {
			return string(unicode.ToUpper(r))
		}
	}
	var first, last rune
	for _, r := range parts[0] {
		first = unicode.ToUpper(r)
		break
	}
	for _, r := range parts[len(parts)-1] {
		last = unicode.ToUpper(r)
		break
	}
	return string(first) + string(last)
}
