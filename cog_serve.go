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
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/sandbox"
	"git.duckfam.us/jj-software/cog/internal/services"
	cogssh "git.duckfam.us/jj-software/cog/internal/ssh"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

func serveCmd(plugins []plugin.Plugin) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Cog server",
		RunE: func(cmd *cobra.Command, args []string) error {
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

			if envBool("COG_LOG_REQUESTS") {
				handler = requestLogger(handler)
			}
			return runService(cmd.Context(), func(ctx context.Context, restart func()) error {
				return runServer(ctx, p, handler, restart)
			})
		},
	}
}

// errRestart ends the server so that it starts again, with the binary an
// update put in place: Main restarts the process once everything is closed.
var errRestart = errors.New("restarting")

// runService runs run until the process is asked to stop: by a signal, or
// by the Windows service manager when it runs as a service. restart, which
// the updater calls, ends it with errRestart.
func runService(parent context.Context, run func(ctx context.Context, restart func()) error) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var restarting atomic.Bool
	restart := func() {
		restarting.Store(true)
		cancel()
	}
	err := runManaged(ctx, func(ctx context.Context) error { return run(ctx, restart) })
	if err == nil && restarting.Load() {
		return errRestart
	}
	return err
}

// runServer starts everything the server runs on p (search, plugins'
// background work, SSH, the updater, certificates) and serves handler
// until ctx ends.
func runServer(ctx context.Context, p *services.Providers, handler http.Handler, restart func()) error {
	cfg := p.Config
	if err := p.StartSearch(ctx); err != nil {
		p.Log.Warn("search init failed", "err", err)
	}
	// Plugin background tasks (scan loops, etc.).
	if err := p.Plugins.Start(ctx); err != nil {
		return fmt.Errorf("start plugins: %w", err)
	}
	// Deliver WebDAV-Push notifications.
	p.StartPush(ctx)
	// Remove cache entries nothing has used in a while.
	p.StartCacheSweep(ctx)
	p.StartLockWatch(ctx)
	startUpdates(ctx, p, restart)

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
	if err := sandbox.Status(); err != nil {
		p.Log.Warn("previews, OCR and media tools run unsandboxed", "reason", err)
	}

	var servers []listener
	if cfg.Addr != "" {
		servers = append(servers, listener{srv: newServer(cfg.Addr, handler)})
	}
	if cfg.TLS.Enabled() {
		m, err := certs.New(cfg.TLS, cfg.DataDir, p.Log)
		if err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		go m.Run(ctx)
		addr := cfg.TLS.Addr
		if addr == "" {
			addr = ":8443"
		}
		srv := newServer(addr, handler)
		srv.TLSConfig = m.TLSConfig()
		servers = append(servers, listener{srv: srv, tls: true})
		if cfg.TLS.HTTPAddr != "" {
			servers = append(servers, listener{srv: newServer(cfg.TLS.HTTPAddr, m.HTTPHandler(redirectToHTTPS(cfg.BaseURL)))})
		}
	}
	if len(servers) == 0 {
		return errors.New("nothing to listen on: set addr, or tls")
	}
	return serve(ctx, servers, p.Log)
}

// redirectToHTTPS sends a plain HTTP request to the same path at baseURL.
func redirectToHTTPS(baseURL string) http.Handler {
	base := strings.TrimSuffix(baseURL, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, base+r.URL.RequestURI(), http.StatusPermanentRedirect)
	})
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

// listener is one of the server's listeners.
type listener struct {
	srv *http.Server
	tls bool // serve TLS with srv.TLSConfig
}

// serve runs servers until ctx ends or one fails, then stops taking
// connections and waits up to shutdownGrace for requests in flight. The
// caller's deferred cleanup (closing search indexes, stopping plugin
// processes) runs after, instead of the process dying under it.
func serve(ctx context.Context, servers []listener, log *slog.Logger) error {
	errc := make(chan error, len(servers))
	for _, l := range servers {
		log.Info("Cog listening", "addr", l.srv.Addr, "tls", l.tls)
		go func() {
			if l.tls {
				errc <- l.srv.ListenAndServeTLS("", "")
			} else {
				errc <- l.srv.ListenAndServe()
			}
		}()
	}
	var failed error
	select {
	case failed = <-errc:
	case <-ctx.Done():
	}
	log.Info("Cog shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	var errs []error
	if failed != nil {
		errs = append(errs, failed)
	}
	for _, l := range servers {
		if err := l.srv.Shutdown(sctx); err != nil {
			errs = append(errs, fmt.Errorf("shutdown %s: %w", l.srv.Addr, err))
		}
	}
	remaining := len(servers)
	if failed != nil {
		remaining--
	}
	for range remaining {
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
