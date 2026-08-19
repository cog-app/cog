package auth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// maxCSRFMultipartMem caps in-memory parsing of multipart bodies during
// CSRF extraction. Files larger than this spill to disk.
const maxCSRFMultipartMem = 32 << 20

// maxCSRFFormBody caps the urlencoded body buffered for rewinding. It matches
// net/http's own ParseForm limit, so nothing that ParseForm would have
// accepted is rejected here.
const maxCSRFFormBody = 10 << 20

// CSRFToken generates a CSRF token from the session ID and a secret.
func CSRFToken(sessionID string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// CSRFTokenFor generates a plugin-scoped CSRF token by including the plugin ID
// in the HMAC input alongside the session ID.
func CSRFTokenFor(sessionID, pluginID string, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(sessionID))
	mac.Write([]byte{0}) // separator to prevent concatenation collisions
	mac.Write([]byte(pluginID))
	return hex.EncodeToString(mac.Sum(nil))
}

// extractCSRFToken returns the CSRF token from either the X-CSRF-Token
// header or the multipart/form body. It deliberately does NOT consult the
// URL query string — r.FormValue would, and a token visible in proxy access
// logs / Referer headers could then be used as a valid CSRF token (audit
// security.md #33).
func extractCSRFToken(r *http.Request) string {
	if t := r.Header.Get("X-CSRF-Token"); t != "" {
		return t
	}
	// PostForm is body-only (no query string). For multipart bodies we have
	// to call ParseMultipartForm explicitly; for urlencoded forms ParseForm
	// is enough.
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(maxCSRFMultipartMem); err != nil {
			return ""
		}
		return r.PostForm.Get("csrf_token")
	}
	// ParseForm drains r.Body. An in-process handler never notices, because it
	// reads the parsed PostForm — but the gRPC bridge forwards the raw body to
	// an out-of-process plugin, and would send it nothing. Buffer and put it
	// back so parsing here stays invisible to whatever handles the request.
	if r.PostForm == nil && r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, maxCSRFFormBody+1))
		if err != nil || len(body) > maxCSRFFormBody {
			return ""
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		defer func() { r.Body = io.NopCloser(bytes.NewReader(body)) }()
	}
	if err := r.ParseForm(); err != nil {
		return ""
	}
	return r.PostForm.Get("csrf_token")
}

// ValidateCSRF checks the CSRF token from the request against the expected value.
func ValidateCSRF(r *http.Request, sessionID string, secret []byte) bool {
	token := extractCSRFToken(r)
	if token == "" {
		return false
	}
	expected := CSRFToken(sessionID, secret)
	return hmac.Equal([]byte(token), []byte(expected))
}

// ValidateCSRFFor checks a plugin-scoped CSRF token from the request.
func ValidateCSRFFor(r *http.Request, sessionID, pluginID string, secret []byte) bool {
	token := extractCSRFToken(r)
	if token == "" {
		return false
	}
	expected := CSRFTokenFor(sessionID, pluginID, secret)
	return hmac.Equal([]byte(token), []byte(expected))
}

// CSRF enforces the token on state-changing requests. The three things it
// needs travel together, which is why they are a struct and not three
// parameters: the secret a token is verified against, the cookie settings a
// rejection's flash is written under, and the logger a rejection is reported
// to. A zero Log falls back to the default logger.
type CSRF struct {
	Secret  []byte
	Cookies cogplugin.CookieConfig
	Log     *slog.Logger
}

// Require wraps a handler and enforces CSRF on state-changing methods.
func (c CSRF) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutation(r.Method) {
			sessID := SessionIDFromContext(r.Context())
			if sessID == "" || !ValidateCSRF(r, sessID, c.Secret) {
				c.reject(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireFor wraps a handler and enforces a plugin-scoped token.
func (c CSRF) RequireFor(pluginID string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutation(r.Method) {
			sessID := SessionIDFromContext(r.Context())
			if sessID == "" || !ValidateCSRFFor(r, sessID, pluginID, c.Secret) {
				c.reject(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutation(method string) bool {
	switch method {
	case "POST", "PUT", "DELETE", "PATCH":
		return true
	}
	return false
}

// reject responds to a CSRF failure. For HTMX requests it sends HX-Refresh to
// trigger a full page reload (which fetches a new token). For regular form
// POSTs it redirects back to the page via GET.
//
// The Referer-based redirect is sanitized to same-origin only — without this,
// an attacker could induce a CSRF-failing request from evil.com and the Referer
// header (attacker-controlled) would land the victim on a phishing page.
// Cross-origin or unparseable Referers fall back to the current path.
//
// A flash goes with the redirect, and the rejection is logged. Neither is
// decoration: a bare 303 back to the form is indistinguishable from the browser
// doing nothing, which is how a page left open past its session expiry reads to
// whoever is submitting it.
func (c CSRF) reject(w http.ResponseWriter, r *http.Request) {
	log := c.Log
	if log == nil {
		log = slog.Default()
	}
	htmx := r.Header.Get("HX-Request") == "true"
	log.Warn("rejected request with invalid CSRF token",
		"method", r.Method, "path", r.URL.Path, "htmx", htmx)

	if htmx {
		w.Header().Set("HX-Refresh", "true")
		http.Error(w, "Forbidden — invalid CSRF token", http.StatusForbidden)
		return
	}
	target := sanitizeReferer(r.Header.Get("Referer"), r.Host)
	if target == "" {
		target = r.URL.Path
	}
	c.Cookies.SetFlash(w, cogplugin.Flash{
		Kind:    "error",
		Message: "That form had expired, so nothing was submitted. Please try again.",
	})
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// sanitizeReferer returns the path portion of referer only when it is
// same-origin with host. Cross-origin, unparseable, or non-absolute
// referers return "". An empty referer also returns "".
func sanitizeReferer(referer, host string) string {
	if referer == "" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil {
		return ""
	}
	// Same-origin requires a host (the Referer is absolute) that equals the
	// request host. We don't validate scheme — relative paths inside the
	// app are fine, but those don't arrive as Referer headers in practice.
	if u.Host == "" || u.Host != host {
		return ""
	}
	target := u.Path
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}
