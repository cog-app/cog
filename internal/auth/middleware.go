package auth

import (
	"context"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
)

type contextKey string

const userContextKey contextKey = "authenticated_user"
const sessionContextKey contextKey = "session_id"

// UserFromContext retrieves the authenticated user from the request context.
func UserFromContext(ctx context.Context) *user.User {
	u, _ := ctx.Value(userContextKey).(*user.User)
	return u
}

// SessionIDFromContext retrieves the session ID from the request context.
func SessionIDFromContext(ctx context.Context) string {
	s, _ := ctx.Value(sessionContextKey).(string)
	return s
}

// Middleware checks authentication in order: session cookie, Basic Auth, Bearer token (app password).
// If authentication succeeds, the user is added to the request context.
// Unauthenticated requests are passed through (handlers decide if auth is required).
type Middleware struct {
	Users        *user.Store
	Sessions     *SessionStore
	AppPasswords *AppPasswordStore
	RateLimiter  *ratelimit.Limiter
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, sessionID := m.authenticate(r)
		if u != nil {
			// If authenticated via Basic Auth (no session), create a session
			// so that CSRF tokens work for web form submissions.
			if sessionID == "" && m.Sessions != nil {
				if sess, err := m.Sessions.Create(u.ID); err == nil {
					sessionID = sess.ID
					http.SetCookie(w, &http.Cookie{
						Name:     SessionCookieName,
						Value:    sess.ID,
						Path:     "/",
						HttpOnly: true,
						SameSite: http.SameSiteLaxMode,
					})
				}
			}
			ctx := context.WithValue(r.Context(), userContextKey, u)
			if sessionID != "" {
				ctx = context.WithValue(ctx, sessionContextKey, sessionID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (m *Middleware) authenticate(r *http.Request) (*user.User, string) {
	// 1. Session cookie
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		if sess, err := m.Sessions.Validate(cookie.Value); err == nil && sess != nil {
			if u, err := m.Users.GetByID(sess.UserID); err == nil {
				return u, sess.ID
			}
		}
	}

	// Rate-limit credential-based auth by client IP.
	ip := ratelimit.ClientIP(r)
	if m.RateLimiter != nil && m.RateLimiter.IsLimited(ip) {
		return nil, ""
	}

	// 2. Basic Auth
	if username, password, ok := r.BasicAuth(); ok {
		// Try user password first
		if u, err := m.Users.Authenticate(username, password); err == nil {
			if m.RateLimiter != nil {
				m.RateLimiter.Reset(ip)
			}
			return u, ""
		}
		// Try app password with username as a hint
		if userID, err := m.AppPasswords.Validate(password); err == nil {
			if u, err := m.Users.GetByID(userID); err == nil {
				if m.RateLimiter != nil {
					m.RateLimiter.Reset(ip)
				}
				return u, ""
			}
		}
		// Basic auth failed — record failure.
		if m.RateLimiter != nil {
			m.RateLimiter.RecordFailure(ip)
		}
	}

	// 3. Bearer token (app password)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		if userID, err := m.AppPasswords.Validate(token); err == nil {
			if u, err := m.Users.GetByID(userID); err == nil {
				if m.RateLimiter != nil {
					m.RateLimiter.Reset(ip)
				}
				return u, ""
			}
		}
		// Bearer auth failed — record failure.
		if m.RateLimiter != nil {
			m.RateLimiter.RecordFailure(ip)
		}
	}

	return nil, ""
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

// RequireAuthWeb wraps a handler and redirects to /login if not authenticated.
func RequireAuthWeb(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin wraps a handler and returns 403 if the user is not an admin.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r.Context())
		if u == nil || !u.IsAdmin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
