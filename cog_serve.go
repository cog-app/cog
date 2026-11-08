package cog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

func serveCmd(plugins []plugin.Plugin) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Cog server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFromEnv()

			// Lets a development server subscribe to feeds on the local
			// network. Never set it in production: it disables the guard
			// that keeps user-supplied URLs off the host's own network.
			if envBool("COG_PRIVATE_IPS") {
				safehttp.SetAllowPrivate(true)
			}

			p, err := openServerProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			handler := web.New(p)

			runSeed(p)

			// Start search indexing after plugins are registered.
			if err := p.StartSearch(context.Background()); err != nil {
				p.Log.Warn("search init failed", "err", err)
			}

			// Start plugin background tasks (scan loops, etc.).
			if err := p.Plugins.Start(context.Background()); err != nil {
				return err
			}
			// Deliver WebDAV-Push notifications.
			p.StartPush(context.Background())
			// Remove cache entries nothing has used in a while.
			p.StartCacheSweep(context.Background())
			p.StartLockWatch(context.Background())

			if envBool("COG_LOG_REQUESTS") {
				handler = requestLogger(handler)
			}

			// Start SSH server if enabled.
			if cfg.SSHEnabled {
				sshSrv, err := cogssh.New(cfg, p, p.SSHKeys, p.Log)
				if err != nil {
					return err
				}
				defer sshSrv.Close()
				go func() {
					if err := sshSrv.ListenAndServe(); err != nil {
						p.Log.Error("ssh server error", "err", err)
					}
				}()
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if err := sandbox.Status(); err != nil {
				p.Log.Warn("previews, OCR and media tools run unsandboxed", "reason", err)
			}
			p.Log.Info("Cog listening", "addr", cfg.Addr)
			return serve(ctx, newServer(cfg.Addr, handler), p.Log)
		},
	}
}

// newServer is the HTTP server for handler. ReadHeaderTimeout stops a client
// holding connections open by sending its headers slowly. There is no overall
// read or write timeout: uploads, downloads and event streams run long.
//
// It speaks unencrypted HTTP/2 beside HTTP/1 on the same port. gRPC needs
// HTTP/2, and Cog serves plain HTTP behind a proxy that terminates TLS, so
// without it a gRPC client could reach Cog only through a proxy that
// re-encrypts.
func newServer(addr string, handler http.Handler) *http.Server {
	var protocols http.Protocols
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		Protocols:         &protocols,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// shutdownGrace is how long requests in flight get to finish once the server
// is asked to stop.
const shutdownGrace = 30 * time.Second

// serve runs srv until ctx ends, then stops taking connections and waits up
// to shutdownGrace for requests in flight. The caller's deferred cleanup
// (closing search indexes, stopping plugin processes) runs after, instead
// of the process dying under it.
func serve(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("Cog shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// credentialHeaders are not written to the request log: a development log
// is still a file that outlives the session it recorded.
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"}

// credentialFields are form and JSON fields whose values the request log
// replaces, by name.
var credentialFields = regexp.MustCompile(`(?i)("?(?:password|passwd|new_password|current_password|old_password|secret|sharedsecret|token|app_password|totp|code)"?\s*[=:]\s*"?)[^&"\s]*`)

// maxLoggedBody is how much of a request or response body the log keeps.
const maxLoggedBody = 64 << 10

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// The body is read into memory to be logged; httputil puts it back.
		shown := r.Clone(r.Context())
		for _, h := range credentialHeaders {
			if shown.Header.Get(h) != "" {
				shown.Header.Set(h, "[redacted]")
			}
		}
		dump, err := httputil.DumpRequest(shown, r.ContentLength >= 0 && r.ContentLength <= maxLoggedBody)
		r.Body = shown.Body
		if err != nil {
			log.Printf("--- REQUEST (dump error: %v) %s %s", err, r.Method, r.URL)
		} else {
			log.Printf("--- REQUEST ---\n%s", credentialFields.ReplaceAll(dump, []byte("${1}[redacted]")))
		}

		rec := &responseRecorder{ResponseWriter: w, statusCode: 200}
		next.ServeHTTP(rec, r)

		log.Printf("--- RESPONSE --- %d %s (%s)\n%s\n",
			rec.statusCode, r.URL, time.Since(start), credentialFields.ReplaceAllString(rec.body.String(), "${1}[redacted]"))
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
	// The log keeps the first maxLoggedBody bytes; a download is not a
	// thing to hold in memory for the sake of a log line.
	if room := maxLoggedBody - r.body.Len(); room > 0 {
		r.body.Write(b[:min(len(b), room)])
	}
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
