package cog

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

func serveCmd(plugins []plugin.Plugin) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Cog server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := configFromEnv()

			p, err := openProviders(plugins...)
			if err != nil {
				return err
			}
			defer p.Close()

			handler := web.New(p)

			// Run seed hook if built with -tags seed and COG_SEED is set.
			if seedHook != nil {
				seedHook(p)
			}

			// Start search indexing after plugins are registered.
			if err := p.StartSearch(context.Background()); err != nil {
				p.Log.Warn("search init failed", "err", err)
			}

			if envBool("COG_LOG_REQUESTS") {
				handler = requestLogger(handler)
			}

			p.Log.Info("Cog listening", "addr", cfg.Addr)
			return http.ListenAndServe(cfg.Addr, handler)
		},
	}
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
