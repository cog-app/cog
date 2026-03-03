package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/compat"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func defaultBaseURL() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	return fmt.Sprintf("http://%s:8580", hostname)
}

func main() {
	var (
		addr       = flag.String("addr", ":8580", "listen address")
		dbPath     = flag.String("db", "system.db", "path to system database")
		createUser = flag.String("create-user", "", "create a user (username:password) and exit")
		baseURL    = flag.String("base-url", defaultBaseURL(), "external base URL")
		logReqs    = flag.Bool("log-requests", false, "log full HTTP request/response bodies")
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

	// WebDAV stub (presence check for NC clients)
	mux.HandleFunc("/remote.php/dav", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/dav/", compat.HandleWebDAVStub)
	mux.HandleFunc("/remote.php/", compat.HandleWebDAVStub)

	// Login Flow v2
	mux.HandleFunc("POST /index.php/login/v2", loginFlow.HandleInit)
	mux.HandleFunc("POST /index.php/login/v2/poll", loginFlow.HandlePoll)
	mux.HandleFunc("GET /index.php/login/v2/grant", loginFlow.HandleGrant)
	mux.HandleFunc("POST /index.php/login/v2/grant", loginFlow.HandleGrant)

	var handler http.Handler = authMW.Wrap(mux)
	if *logReqs {
		handler = requestLogger(handler)
	}

	log.Printf("Cog listening on %s", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		log.Fatal(err)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Dump the full incoming request (headers + body)
		dump, err := httputil.DumpRequest(r, true)
		if err != nil {
			log.Printf("--- REQUEST (dump error: %v) %s %s", err, r.Method, r.URL)
		} else {
			log.Printf("--- REQUEST ---\n%s", dump)
		}

		// r.Body is consumed by DumpRequest; restore it so handlers can read it
		// DumpRequest already handles this internally, but if body was form-encoded
		// we need the parsed form to work. Re-parse from the dump.
		// Actually httputil.DumpRequest replaces the body with a new reader, so we're fine.

		// Wrap the ResponseWriter to capture status code and body
		rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(rec, r)

		log.Printf("--- RESPONSE --- %d %s (%s)\n%s\n",
			rec.statusCode, r.URL, time.Since(start), rec.body.String())
	})
}

type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       strings.Builder
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

// Ensure Flush is proxied if the underlying writer supports it.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Keep io.ReaderFrom working for large responses.
func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	return io.Copy(r.ResponseWriter, src)
}
