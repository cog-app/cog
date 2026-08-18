package plugin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// CSRFToken returns the CSRF token from the request context.
func CSRFToken(r *http.Request) string {
	return CSRFTokenFromContext(r.Context())
}

// UserDB returns the per-user plugin database from the request context.
// The plugin DB provider must be injected via WithPluginDB. A request missing
// either half is a wiring fault, reported rather than dereferenced.
func UserDB(r *http.Request) (*sql.DB, error) {
	u := UserFromContext(r.Context())
	if u == nil {
		return nil, errors.New("plugin: request has no authenticated user")
	}
	fn := pluginDBFromContext(r.Context())
	if fn == nil {
		return nil, errors.New("plugin: request has no plugin database provider")
	}
	return fn(u.Username)
}

// SetTitle sets the page title header for the shell middleware.
func SetTitle(w http.ResponseWriter, title string) {
	w.Header().Set("X-Page-Title", title)
}

// Username returns the authenticated user's username from the request context,
// or "" if the request carries no user.
func Username(r *http.Request) string {
	u := UserFromContext(r.Context())
	if u == nil {
		return ""
	}
	return u.Username
}

// WriteJSON encodes v as JSON and writes it to w with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("json encode", "err", err)
	}
}

// PathID extracts an int64 ID from a URL path after the given prefix.
// E.g. PathID("/api/notes/42/content", "/api/notes/") returns 42.
func PathID(path, prefix string) int64 {
	s := strings.TrimPrefix(path, prefix)
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// PathIDSuffix extracts an int64 ID from a URL path between a prefix and suffix.
func PathIDSuffix(path, prefix, suffix string) int64 {
	s := strings.TrimPrefix(path, prefix)
	s = strings.TrimSuffix(s, suffix)
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}

// pluginDB context key and helpers

const pluginDBContextKey contextKeyType = "plugin_db_func"

type pluginDBFunc func(username string) (*sql.DB, error)

// WithPluginDB returns a context with a plugin DB provider function.
func WithPluginDB(ctx context.Context, fn func(username string) (*sql.DB, error)) context.Context {
	return context.WithValue(ctx, pluginDBContextKey, pluginDBFunc(fn))
}

func pluginDBFromContext(ctx context.Context) func(username string) (*sql.DB, error) {
	fn, _ := ctx.Value(pluginDBContextKey).(pluginDBFunc)
	return fn
}
