// Package bodylimit caps every request body by default. A handler that
// stores what it receives, such as a file upload, says so where its route is
// registered, and is then bounded by quota instead. Without a default, a
// handler that forgot its own limit would buffer, parse or spill to disk
// whatever a client sent.
package bodylimit

import (
	"context"
	"io"
	"net/http"
)

// Default is the limit for a request no route raised. It is far above any
// form, JSON or XML body, and above the imports that read a whole file into
// memory (data import, OPML, bookmarks).
const Default = 64 << 20

type ctxKey struct{}

// body is http.MaxBytesReader with a limit that Unlimited can lift.
type body struct {
	rc    io.ReadCloser
	limit int64 // < 0 for none
	left  int64
	err   error
}

func (b *body) Read(p []byte) (int, error) {
	if b.limit < 0 {
		return b.rc.Read(p)
	}
	if b.err != nil {
		return 0, b.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	// Read one byte past the limit, to tell a body of exactly the limit
	// from a longer one.
	if int64(len(p))-1 > b.left {
		p = p[:b.left+1]
	}
	n, err := b.rc.Read(p)
	if int64(n) <= b.left {
		b.left -= int64(n)
		b.err = err
		return n, err
	}
	n, b.left = int(b.left), 0
	b.err = &http.MaxBytesError{Limit: b.limit}
	return n, b.err
}

func (b *body) Close() error { return b.rc.Close() }

// Middleware caps each request body at limit: reading past it fails with an
// *http.MaxBytesError.
func Middleware(limit int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody {
			next.ServeHTTP(w, r)
			return
		}
		b := &body{rc: r.Body, limit: limit, left: limit}
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, b))
		r.Body = b
		next.ServeHTTP(w, r)
	})
}

// Unlimited lifts the default limit for next, which must bound what it
// stores some other way, such as by quota. It wraps the route as registered,
// outside any middleware that reads the body (CSRF reads a form's token).
func Unlimited(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := r.Context().Value(ctxKey{}).(*body); ok {
			b.limit = -1
		}
		next.ServeHTTP(w, r)
	})
}
