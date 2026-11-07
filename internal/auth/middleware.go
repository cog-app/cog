package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

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
	Guard        *ratelimit.Guard
	Cookies      cogplugin.CookieConfig
	// PluginOriginHost says whether a request's host is a plugin origin
	// (D14). On one, only a frame token signs a user in; the host's
	// session cookie, Basic and Bearer credentials are not consulted.
	PluginOriginHost func(host string) (pluginID string, ok bool)
	// FrameKey signs frame tokens.
	FrameKey []byte
	// Now is the clock frame tokens expire by; time.Now when nil.
	Now func() time.Time
}

func (m *Middleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.PluginOriginHost != nil {
			if originPlugin, ok := m.PluginOriginHost(r.Host); ok {
				m.serveFrame(w, r, next, originPlugin)
				return
			}
		}
		u, sess := m.authenticate(r)
		if u != nil {
			// What a signed-in user is sent is theirs: no shared cache may
			// keep it for the next client, whatever URL it came from. A
			// handler may still allow caching in the browser.
			w.Header().Set("Cache-Control", "private")
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

// serveFrame authenticates a request on a plugin origin by its frame token:
// the one in the URL the shell handed the frame, or the one the origin's
// cookie keeps. The token must name the plugin the path belongs to, and the
// origin's plugin when there is one per plugin.
func (m *Middleware) serveFrame(w http.ResponseWriter, r *http.Request, next http.Handler, originPlugin string) {
	pathPlugin := PluginIDFromPath(r.URL.Path)
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	admit := func(token string) (*user.User, bool) {
		userID, pluginID, ok := ParseFrameToken(m.FrameKey, token, now)
		if !ok || pluginID != pathPlugin || (originPlugin != "" && pluginID != originPlugin) {
			return nil, false
		}
		u, err := m.Users.GetByID(userID)
		logAuthErr("frame user", err)
		return u, u != nil
	}
	var u *user.User
	token := r.URL.Query().Get(FrameParam)
	if token != "" {
		var ok bool
		if u, ok = admit(token); ok {
			// The handoff is spent on a cookie of the origin's own. It is
			// sent cross-site, from inside the shell's frame, so SameSite
			// is None; browsers then require Secure, which the origin is.
			token = FrameToken(m.FrameKey, u.ID, pathPlugin, now.Add(FrameCookieTTL))
			http.SetCookie(w, &http.Cookie{
				Name: FrameCookieName, Value: token, Path: "/apps/" + pathPlugin + "/",
				MaxAge: int(FrameCookieTTL.Seconds()), HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode,
			})
		}
	} else if c, err := r.Cookie(FrameCookieName); err == nil {
		u, _ = admit(c.Value)
	}
	if u == nil {
		next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private")
	ctx := context.WithValue(r.Context(), userContextKey, u)
	ctx = context.WithValue(ctx, sessionContextKey, token)
	next.ServeHTTP(w, r.WithContext(ctx))
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

	ip := ratelimit.ClientIP(r)

	// 2. Basic Auth. Without a username it names no account: it is a
	// public link's password, which the link's handler checks.
	if username, password, ok := r.BasicAuth(); ok && username != "" {
		var u *user.User
		_, err := m.Guard.Try(ip, ratelimit.AccountKey(username), func() bool {
			u = m.basicUser(username, password)
			return u != nil
		})
		logAuthErr("basic auth", err)
		return u, nil
	}

	// 3. Bearer token (app password)
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		var u *user.User
		_, err := m.Guard.Try(ip, "", func() bool {
			u = m.appPasswordUser(strings.TrimPrefix(auth, "Bearer "))
			return u != nil
		})
		logAuthErr("bearer auth", err)
		return u, nil
	}

	return nil, nil
}

// basicUser returns the user Basic credentials sign in as: by their
// password, or by an app password of theirs. An app password signs in only
// as its own user, whatever username it is sent with.
func (m *Middleware) basicUser(username, password string) *user.User {
	u, err := m.Users.Authenticate(username, password)
	logAuthErr("basic auth", err)
	if u != nil && u.MFAMethod == "" {
		return u
	}
	// A password alone is only the first of an MFA user's factors, and
	// Basic auth has no way to ask for the second. Their clients sign in
	// with an app password, which Login Flow issues after both.
	if u = m.appPasswordUser(password); u != nil && strings.EqualFold(u.Username, username) {
		return u
	}
	return nil
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
	if err == nil || errors.Is(err, user.ErrAuthFailed) || errors.Is(err, user.ErrNotFound) || errors.Is(err, ErrInvalidAppPassword) || errors.Is(err, ratelimit.ErrLimited) {
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
