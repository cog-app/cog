package compat

import (
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// NCCompat holds dependencies for NextCloud-compatible OCS/API endpoints.
type NCCompat struct {
	Users   *user.Store
	Files   *files.Plugin
	DataDir string
}

// RegisterRoutes registers all NextCloud-compatible OCS and helper routes.
func (nc *NCCompat) RegisterRoutes(mux *http.ServeMux) {
	// Connectivity check (NC clients use GET /index.php/204 to test)
	mux.HandleFunc("GET /index.php/204", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Avatar (serves real uploaded avatar or initials SVG)
	mux.HandleFunc("GET /index.php/avatar/{userID}/{size}", nc.HandleAvatar)

	// Notifications (no notification system yet — empty list is correct)
	handleNotifications := func(w http.ResponseWriter, r *http.Request) {
		writeOCS(w, http.StatusOK, []any{})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/notifications/api/v2/notifications", handleNotifications)
	mux.HandleFunc("GET /ocs/v1.php/apps/notifications/api/v2/notifications", handleNotifications)

	// Recommendations (no recommendation engine — empty list is correct)
	handleRecommendations := func(w http.ResponseWriter, r *http.Request) {
		writeOCS(w, http.StatusOK, []any{})
	}
	mux.HandleFunc("GET /ocs/v2.php/apps/recommendations/api/v1/recommendations", handleRecommendations)
	mux.HandleFunc("GET /ocs/v1.php/apps/recommendations/api/v1/recommendations", handleRecommendations)

	// User status (returns authenticated user's status)
	mux.Handle("GET /ocs/v2.php/apps/user_status/api/v1/user_status", auth.RequireAuth(http.HandlerFunc(nc.HandleUserStatus)))
	mux.Handle("GET /ocs/v1.php/apps/user_status/api/v1/user_status", auth.RequireAuth(http.HandlerFunc(nc.HandleUserStatus)))

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

	u, err := nc.Users.GetByID(userID)
	if err != nil {
		// Try by username (NC clients may use either)
		u, err = nc.Users.GetByUsername(userID)
	}
	if err != nil {
		serveInitialsSVG(w, "?", userID)
		return
	}

	if u.Avatar != "" {
		avatarPath := filepath.Join(nc.DataDir, "avatars", u.Avatar)
		if f, err := os.Open(avatarPath); err == nil {
			defer f.Close()
			ext := filepath.Ext(u.Avatar)
			switch ext {
			case ".png":
				w.Header().Set("Content-Type", "image/png")
			default:
				w.Header().Set("Content-Type", "image/jpeg")
			}
			w.Header().Set("Cache-Control", "public, max-age=3600")
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
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	writeOCS(w, http.StatusOK, map[string]any{
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
	w.Header().Set("Cache-Control", "public, max-age=3600")
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="64" viewBox="0 0 64 64">
  <circle cx="32" cy="32" r="32" fill="%s"/>
  <text x="32" y="32" dy=".35em" text-anchor="middle" fill="white" font-family="sans-serif" font-size="24" font-weight="600">%s</text>
</svg>`, color, init)
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
