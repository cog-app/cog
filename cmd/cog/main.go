package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func main() {
	var (
		addr       = flag.String("addr", ":8080", "listen address")
		dbPath     = flag.String("db", "system.db", "path to system database")
		createUser = flag.String("create-user", "", "create a user (username:password) and exit")
		baseURL    = flag.String("base-url", "http://localhost:8080", "external base URL")
	)
	flag.Parse()

	sysDB, err := db.OpenSystem(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open system database: %v", err)
	}
	defer sysDB.Close()

	users := user.NewStore(sysDB)
	sessions := auth.NewSessionStore(sysDB)
	appPasswords := auth.NewAppPasswordStore(sysDB)

	// Handle --create-user
	if *createUser != "" {
		parts := strings.SplitN(*createUser, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Usage: --create-user username:password\n")
			os.Exit(1)
		}
		u, err := users.Create(parts[0], parts[1])
		if err != nil {
			log.Fatalf("Failed to create user: %v", err)
		}
		fmt.Printf("Created user %q (id: %s)\n", u.Username, u.ID)
		return
	}

	// Auth middleware
	authMW := &auth.Middleware{
		Users:        users,
		Sessions:     sessions,
		AppPasswords: appPasswords,
	}

	// Login Flow v2
	loginFlow := compat.NewLoginFlowHandler(users, appPasswords, sessions, *baseURL)

	// Periodic cleanup of expired login flows
	go func() {
		for range time.Tick(time.Minute) {
			loginFlow.Cleanup()
		}
	}()

	// Routes
	mux := http.NewServeMux()

	// Status & OCS
	mux.HandleFunc("GET /status.php", compat.HandleStatus)
	mux.HandleFunc("GET /ocs/v1.php/cloud/capabilities", compat.HandleCapabilities)
	mux.HandleFunc("GET /ocs/v2.php/cloud/capabilities", compat.HandleCapabilities)
	mux.Handle("GET /ocs/v1.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))
	mux.Handle("GET /ocs/v2.php/cloud/user", auth.RequireAuth(http.HandlerFunc(compat.HandleCurrentUser)))

	// Login Flow v2
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	handler := authMW.Wrap(mux)

	log.Printf("Cog listening on %s", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}
