// Package cog provides a convenience entry point for building custom Cog
// distributions with additional plugins.
package cog

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"os"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

func defaultBaseURL() string {
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "localhost"
	}
	return fmt.Sprintf("http://%s:8580", hostname)
}

// Config holds server configuration for custom Cog builds.
type Config = config.Config

// Main starts a Cog server with optional additional plugins.
// Protected plugins (dashboard, files) are always included.
func Main(plugins ...plugin.Plugin) {
	var (
		addr       = flag.String("addr", ":8580", "listen address")
		dbPath     = flag.String("db", "system.db", "path to system database")
		dataDir    = flag.String("data-dir", "data", "root directory for user data")
		createUser = flag.String("create-user", "", "create a user (username:password) and exit")
		baseURL    = flag.String("base-url", defaultBaseURL(), "external base URL")
		logReqs    = flag.Bool("log-requests", false, "log full HTTP request/response bodies")
		smtpHost   = flag.String("smtp-host", "", "SMTP server hostname")
		smtpPort   = flag.Int("smtp-port", 587, "SMTP server port")
		smtpUser   = flag.String("smtp-user", "", "SMTP username")
		smtpPass   = flag.String("smtp-pass", "", "SMTP password")
		smtpFrom   = flag.String("smtp-from", "", "SMTP from address")
	)
	flag.Parse()

	cfg := config.Config{
		Addr:     *addr,
		DBPath:   *dbPath,
		DataDir:  *dataDir,
		BaseURL:  *baseURL,
		SMTPHost: *smtpHost,
		SMTPPort: *smtpPort,
		SMTPUser: *smtpUser,
		SMTPPass: *smtpPass,
		SMTPFrom: *smtpFrom,
	}

	p, err := services.New(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize: %v", err)
	}
	defer p.Close()

	// Handle --create-user
	if *createUser != "" {
		parts := strings.SplitN(*createUser, ":", 2)
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "Usage: --create-user username:password\n")
			os.Exit(1)
		}
		u, err := p.Users.Create(parts[0], parts[1])
		if err != nil {
			log.Fatalf("Failed to create user: %v", err)
		}
		// Create root mount pointing to default volume.
		if p.Volumes != nil && p.Mounts != nil {
			if defVol, volErr := p.Volumes.GetByName("default"); volErr == nil {
				subpath := u.Username + "/files"
				p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false)
			}
		}
		fmt.Printf("Created user %q (id: %s)\n", u.Username, u.ID)
		return
	}

	handler := web.New(p)

	// Start search indexing after plugins are registered.
	if err := p.StartSearch(context.Background()); err != nil {
		log.Printf("Warning: search init failed: %v", err)
	}

	if *logReqs {
		handler = requestLogger(handler)
	}

	log.Printf("Cog listening on %s", cfg.Addr)
	if err := http.ListenAndServe(cfg.Addr, handler); err != nil {
		log.Fatal(err)
	}
}

// Run starts a Cog server with custom context, config and error handling. See [Main] for using a default installation with custom plugins.
func Run(ctx context.Context, cfg Config, plugins ...plugin.Plugin) error {
	p, err := services.New(cfg)
	if err != nil {
		return err
	}
	defer p.Close()

	handler := web.New(p, plugins...)

	if err := p.StartSearch(ctx); err != nil {
		log.Printf("Warning: search init failed: %v", err)
	}

	log.Printf("Cog listening on %s", cfg.Addr)
	return http.ListenAndServe(cfg.Addr, handler)
}
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		dump, err := httputil.DumpRequest(r, true)
		if err != nil {
			log.Printf("--- REQUEST (dump error: %v) %s %s", err, r.Method, r.URL)
		} else {
			log.Printf("--- REQUEST ---\n%s", dump)
		}

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

func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (r *responseRecorder) ReadFrom(src io.Reader) (int64, error) {
	return io.Copy(r.ResponseWriter, src)
}
