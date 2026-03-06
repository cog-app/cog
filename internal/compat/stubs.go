package compat

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// HandleAvatar returns a placeholder avatar image.
// Route: GET /index.php/avatar/{user_id}/{size}
func HandleAvatar(w http.ResponseWriter, r *http.Request) {
	// Generate a 1x1 gray PNG as a placeholder
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 120, G: 120, B: 120, A: 255})
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	png.Encode(w, img)
}

// HandleNotifications returns an empty notifications list.
// Route: GET /ocs/v2.php/apps/notifications/api/v2/notifications
func HandleNotifications(w http.ResponseWriter, r *http.Request) {
	writeOCS(w, http.StatusOK, []any{})
}

// HandleRecommendations returns an empty recommendations list.
// Route: GET /ocs/v2.php/apps/recommendations/api/v1/recommendations
func HandleRecommendations(w http.ResponseWriter, r *http.Request) {
	writeOCS(w, http.StatusOK, []any{})
}

// HandleUserStatus returns a default user status.
// Route: GET /ocs/v2.php/apps/user_status/api/v1/user_status
func HandleUserStatus(w http.ResponseWriter, r *http.Request) {
	writeOCS(w, http.StatusOK, map[string]any{
		"userId":              "",
		"message":             nil,
		"messageId":           nil,
		"messageIsPredefined": false,
		"icon":                nil,
		"clearAt":             nil,
		"status":              "online",
		"statusIsUserDefined": false,
	})
}

// HandleConnectivityCheck returns 204 No Content.
// NC clients use GET /index.php/204 to test connectivity.
func HandleConnectivityCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
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
		w.WriteHeader(http.StatusNotFound)
		return
	}

	meta, err := h.Files.GetByID(u.Username, fileID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
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
		w.WriteHeader(http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	io.Copy(w, f)
}

// HandleWellKnownCalDAV redirects to the DAV endpoint for CalDAV discovery.
func HandleWellKnownCalDAV(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/remote.php/dav/", http.StatusMovedPermanently)
}

// HandleWellKnownCardDAV redirects to the DAV endpoint for CardDAV discovery.
func HandleWellKnownCardDAV(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/remote.php/dav/", http.StatusMovedPermanently)
}

// RegisterStubs registers all stub/TODO routes on the given mux.
// These are minimal responses to keep NC clients happy while
// real implementations are built out.
func RegisterStubs(mux *http.ServeMux) {
	// Connectivity check
	mux.HandleFunc("GET /index.php/204", HandleConnectivityCheck)

	// Avatar
	mux.HandleFunc("GET /index.php/avatar/{userID}/{size}", HandleAvatar)

	// Notifications
	mux.HandleFunc("GET /ocs/v2.php/apps/notifications/api/v2/notifications", HandleNotifications)
	mux.HandleFunc("GET /ocs/v1.php/apps/notifications/api/v2/notifications", HandleNotifications)

	// Recommendations
	mux.HandleFunc("GET /ocs/v2.php/apps/recommendations/api/v1/recommendations", HandleRecommendations)
	mux.HandleFunc("GET /ocs/v1.php/apps/recommendations/api/v1/recommendations", HandleRecommendations)

	// User status
	mux.HandleFunc("GET /ocs/v2.php/apps/user_status/api/v1/user_status", HandleUserStatus)
	mux.HandleFunc("GET /ocs/v1.php/apps/user_status/api/v1/user_status", HandleUserStatus)

	// .well-known discovery redirects
	mux.HandleFunc("GET /.well-known/caldav", HandleWellKnownCalDAV)
	mux.HandleFunc("PROPFIND /.well-known/caldav", HandleWellKnownCalDAV)
	mux.HandleFunc("GET /.well-known/carddav", HandleWellKnownCardDAV)
	mux.HandleFunc("PROPFIND /.well-known/carddav", HandleWellKnownCardDAV)

	fmt.Println("Registered stub routes: avatar, notifications, recommendations, user_status, connectivity check, well-known")
}
