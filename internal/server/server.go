// Package server wires up the Cog HTTP handler.
package server

import (
	"database/sql"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// Config holds the dependencies needed to build the handler.
type Config struct {
	DB      *sql.DB
	BaseURL string
}

// New builds the full HTTP handler with all routes and middleware.
func New(cfg Config) http.Handler {
	users := user.NewStore(cfg.DB)
	sessions := auth.NewSessionStore(cfg.DB)
	appPasswords := auth.NewAppPasswordStore(cfg.DB)

	authMW := &auth.Middleware{
		Users:        users,
		Sessions:     sessions,
		AppPasswords: appPasswords,
	}

	loginFlow := compat.NewLoginFlowHandler(users, appPasswords, sessions, cfg.BaseURL)

	mux := http.NewServeMux()

	// Status & OCS
	mux.HandleFunc("GET /status.php", compat.HandleStatus)
	mux.HandleFunc("GET /ocs/v1.php/cloud/capabilities", compat.HandleCapabilities)
	mux.HandleFunc("GET /ocs/v2.php/cloud/capabilities", compat.HandleCapabilities)
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))

	// WebDAV stub (presence check for NC clients)
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/", compat.HandleWebDAVStub)

	// Stub routes (avatar, notifications, connectivity, etc.)
	compat.RegisterStubs(mux)

	// Login Flow v2
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	return authMW.Wrap(mux)
}
