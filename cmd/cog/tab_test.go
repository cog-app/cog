package main_test

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
)

type statusLogger struct {
	next http.Handler
	log  *slog.Logger
}

func (h *statusLogger) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, statusCode: 200}
	h.next.ServeHTTP(rec, r)
	if r.URL.Path == "/login" {
		h.log.Info("request", "method", r.Method, "url", r.URL.String(), "status", rec.statusCode)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func TestTabReuse(t *testing.T) {
	env := cogtest.New(t, allPlugins...)
	logged := &statusLogger{next: env, log: slog.New(slog.NewTextHandler(t.Output(), nil))}
	eng := webtest.New(logged)
	t.Cleanup(eng.Close)

	for i := range 4 {
		env.CreateUser(t, fmt.Sprintf("user%d", i), "pass")
	}

	for i := range 4 {
		user := fmt.Sprintf("user%d", i)
		t.Run(fmt.Sprintf("tab-%d", i), func(t *testing.T) {
			b := eng.Start(t)
			b.Navigate(t, "/login")
			b.Type(t, "#username", user)
			b.Type(t, "#password", "pass")
			b.Click(t, `button[type="submit"]`)
			time.Sleep(500 * time.Millisecond)
			url := b.URL(t)
			t.Logf("tab %d: URL=%s", i, url)
		})
	}
}
