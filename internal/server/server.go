// Package server wires up the Cog HTTP handler.
package server

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/plugin/files"
	"git.duckfam.us/jj-software/cog/internal/services"
)

// New builds the full HTTP handler with all routes and middleware.
func New(p *services.Providers) http.Handler {
	authMW := &auth.Middleware{
		Users:        p.Users,
		Sessions:     p.Sessions,
		AppPasswords: p.AppPasswords,
	}

	loginFlow := compat.NewLoginFlowHandler(p.Users, p.AppPasswords, p.Sessions, p.Config.BaseURL)

	mux := http.NewServeMux()

	// Status & OCS
	mux.HandleFunc("GET /status.php", compat.HandleStatus)
	mux.HandleFunc("GET /ocs/v1.php/cloud/capabilities", compat.HandleCapabilities)
	mux.HandleFunc("GET /ocs/v2.php/cloud/capabilities", compat.HandleCapabilities)
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))

	// WebDAV file operations (NC clients)
	webdav := &compat.WebDAVHandler{Files: p.Files, Users: p.Users}
	mux.Handle("/remote.php/dav/files/", auth.RequireAuth(webdav))

	// Direct download API (NC Android app uses this instead of WebDAV GET)
	mux.Handle("POST /ocs/v2.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))
	mux.Handle("POST /ocs/v1.php/apps/dav/api/v1/direct", auth.RequireAuth(webdav.HandleDirectDownload(p.Config.BaseURL)))

	// Token-based direct file serving (no auth — token is the credential)
	mux.HandleFunc("GET /remote.php/dav/direct/", webdav.HandleDirectServe)

	// WebDAV stub for non-file DAV paths (presence check, CalDAV, etc.)
	// Only exact /remote.php/dav for HEAD/PROPFIND presence checks.
	// The /remote.php/dav/ prefix catches unhandled sub-paths (uploads, CalDAV)
	// but rejects write methods so uploads aren't silently swallowed.
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)

	// Stub routes (avatar, notifications, connectivity, etc.)
	compat.RegisterStubs(mux)

	// Login Flow v2
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	// Files & Sharing API
	filesAPI := files.NewAPI(p.Files, p.Shares, p.Users)
	filesAPI.RegisterRoutes(mux)

	// Register and start plugins
	p.Plugins.Add(p.Files)

	return authMW.Wrap(mux)
}
