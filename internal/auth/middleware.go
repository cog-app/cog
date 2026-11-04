package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
	cogplugin "git.duckfam.us/jj-software/cog/plugin"
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
	Cookies      cogplugin.CookieConfig
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, sess := m.authenticate(r)
		if u != nil {
			var sessionID string
			switch {
			case sess != nil:
				sessionID = sess.ID
				renewed, err := m.Sessions.Renew(sess)
				if err != nil {
					slog.Error("auth: renew session", "user", u.ID, "err", err)
				}
				if renewed {
					sess.SetCookie(w, m.Cookies)
				}
			case m.Sessions != nil && wantsPage(r):
				// A browser signed in with Basic Auth gets a session so the
				// forms on the page it is loading carry a CSRF token. DAV,
				// OCS and API clients never submit those forms, and many
				// discard cookies, so a session per request would only pile
				// up rows.
				if sess, err := m.Sessions.Start(u.ID, r); err != nil {
					slog.Error("auth: create session for basic auth", "user", u.ID, "err", err)
				} else {
					sessionID = sess.ID
					sess.SetCookie(w, m.Cookies)
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

// wantsPage reports whether r is a browser loading a page or an HTMX
// fragment, as opposed to a sync or API client.
func wantsPage(r *http.Request) bool {
	return r.Header.Get("HX-Request") != "" || strings.Contains(r.Header.Get("Accept"), "text/html")
}

// authenticate returns the request's user, and its session when the user was
// authenticated by the session cookie.
func (m *Middleware) authenticate(r *http.Request) (*user.User, *Session) {
	// 1. Session cookie
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		sess, err := m.Sessions.Validate(cookie.Value)
		logAuthErr("validate session", err)
		if sess != nil {
			u, err := m.Users.GetByID(sess.UserID)
			logAuthErr("session user", err)
			if u != nil {
				return u, sess
			}
		}
	}

	// Rate-limit credential-based auth by client IP.
	ip := ratelimit.ClientIP(r)
	if m.RateLimiter != nil && m.RateLimiter.IsLimited(ip) {
		return nil, nil
	}

	// 2. Basic Auth
	if username, password, ok := r.BasicAuth(); ok {
		// Try user password first
		u, err := m.Users.Authenticate(username, password)
		logAuthErr("basic auth", err)
		if u == nil {
			// Try app password with username as a hint
			u = m.appPasswordUser(password)
		}
		if u != nil {
			if m.RateLimiter != nil {
				m.RateLimiter.Reset(ip)
			}
			return u, nil
		}
		// Basic auth failed — record failure.
		if m.RateLimiter != nil {
			m.RateLimiter.RecordFailure(ip)
		}
	}

	// 3. Bearer token (app password)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if u := m.appPasswordUser(strings.TrimPrefix(auth, "Bearer ")); u != nil {
			if m.RateLimiter != nil {
				m.RateLimiter.Reset(ip)
			}
			return u, nil
		}
		// Bearer auth failed — record failure.
		if m.RateLimiter != nil {
			m.RateLimiter.RecordFailure(ip)
		}
	}

	return nil, nil
}

// appPasswordUser returns the user an app password belongs to, or nil.
func (m *Middleware) appPasswordUser(token string) *user.User {
	userID, err := m.AppPasswords.Validate(token)
	if err != nil {
		logAuthErr("validate app password", err)
		return nil
	}
	u, err := m.Users.GetByID(userID)
	logAuthErr("app password user", err)
	return u
}

// logAuthErr logs err unless it only says the credentials were wrong. The
// request still goes on unauthenticated: the middleware cannot fail it, and
// a handler that needs a user rejects it.
func logAuthErr(step string, err error) {
	if err == nil || errors.Is(err, user.ErrAuthFailed) || errors.Is(err, user.ErrNotFound) || errors.Is(err, ErrInvalidAppPassword) {
		return
	}
	slog.Error("auth: "+step, "err", err)
}

// RequireAuth wraps a handler and returns 401 if no user is authenticated.
// Browser requests are redirected to /login; HTMX requests get HX-Redirect;
// programmatic clients (with Authorization header) get 401 + WWW-Authenticate.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			switch {
			case r.Header.Get("HX-Request") != "":
				w.Header().Set("HX-Redirect", loginURL(r))
				w.WriteHeader(http.StatusUnauthorized)
			case isBrowserNavigation(r):
				// Browser page navigation — redirect to login form.
				http.Redirect(w, r, loginURL(r), http.StatusSeeOther)
			default:
				// API/programmatic client — 401 with Basic challenge.
				w.Header().Set("WWW-Authenticate", `Basic realm="Cog"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAuthWeb wraps a handler and redirects to /login if not authenticated.
func RequireAuthWeb(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if UserFromContext(r.Context()) == nil {
			http.Redirect(w, r, loginURL(r), http.StatusSeeOther)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loginURL returns "/login?next=<current_url>" for the given request.
func loginURL(r *http.Request) string {
	return "/login?next=" + url.QueryEscape(r.URL.RequestURI())
}

// isBrowserNavigation returns true if the request looks like a browser page
// navigation. Requests with an Authorization header are always considered
// programmatic, even if they accept HTML.
func isBrowserNavigation(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
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
