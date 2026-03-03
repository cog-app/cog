package auth

import (
	"context"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/user"
)

type contextKey string

const userContextKey contextKey = "authenticated_user"

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) *user.User {
	u, _ := ctx.Value(userContextKey).(*user.User)
	return u
}

// Middleware checks authentication in order: session cookie, Basic Auth, Bearer token (app password).
// If authentication succeeds, the user is added to the request context.
// Unauthenticated requests are passed through (handlers decide if auth is required).
type Middleware struct {
	Users        *user.Store
	Sessions     *SessionStore
	AppPasswords *AppPasswordStore
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u := m.authenticate(r); u != nil {
			ctx := context.WithValue(r.Context(), userContextKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) authenticate(r *http.Request) *user.User {
	// 1. Session cookie
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if sess, err := m.Sessions.Validate(cookie.Value); err == nil && sess != nil {
			if u, err := m.Users.GetByID(sess.UserID); err == nil {
				return u
			}
		}
	}

	// 2. Basic Auth
	if username, password, ok := r.BasicAuth(); ok {
		// Try user password first
		if u, err := m.Users.Authenticate(username, password); err == nil {
			return u
		}
		// Try app password with username as a hint
		if userID, err := m.AppPasswords.Validate(password); err == nil {
			if u, err := m.Users.GetByID(userID); err == nil {
				return u
			}
		}
	}

	// 3. Bearer token (app password)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		if userID, err := m.AppPasswords.Validate(token); err == nil {
			if u, err := m.Users.GetByID(userID); err == nil {
				return u
			}
		}
	}

	return nil
}

// RequireAuth wraps a handler and returns 401 if no user is authenticated.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
