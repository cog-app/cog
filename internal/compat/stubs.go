package compat

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
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
		"userId":        "",
		"message":       nil,
		"messageId":     nil,
		"messageIsPredefined": false,
		"icon":          nil,
		"clearAt":       nil,
		"status":        "online",
		"statusIsUserDefined": false,
	})
}

// HandleConnectivityCheck returns 204 No Content.
// NC clients use GET /index.php/204 to test connectivity.
func HandleConnectivityCheck(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// HandlePreview returns a 404 with a proper OCS error for file previews.
// NC clients request GET /index.php/core/preview?fileId=N for thumbnails.
// Returning a clean 404 prevents the app from caching fileId=-1.
func HandlePreview(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
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

	// Preview (not implemented — nc:has-preview=false tells clients not to ask)
	mux.HandleFunc("GET /index.php/core/preview", HandlePreview)

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
	mux.HandleFunc("GET /.well-known/carddav", HandleWellKnownCardDAV)

	fmt.Println("Registered stub routes: avatar, notifications, recommendations, user_status, preview, connectivity check, well-known")
}
