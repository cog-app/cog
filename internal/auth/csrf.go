package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

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

// ValidateCSRF checks the CSRF token from the request against the expected value.
func ValidateCSRF(r *http.Request, sessionID string, secret []byte) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	if token == "" {
		return false
	}
	expected := CSRFToken(sessionID, secret)
	return hmac.Equal([]byte(token), []byte(expected))
}

// ValidateCSRFFor checks a plugin-scoped CSRF token from the request.
func ValidateCSRFFor(r *http.Request, sessionID, pluginID string, secret []byte) bool {
	token := r.Header.Get("X-CSRF-Token")
	if token == "" {
		token = r.FormValue("csrf_token")
	}
	if token == "" {
		return false
	}
	expected := CSRFTokenFor(sessionID, pluginID, secret)
	return hmac.Equal([]byte(token), []byte(expected))
}

// RequireCSRFFor wraps a handler and enforces plugin-scoped CSRF on state-changing methods.
func RequireCSRFFor(pluginID string, secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
			sessID := SessionIDFromContext(r.Context())
			if sessID == "" || !ValidateCSRFFor(r, sessID, pluginID, secret) {
				csrfReject(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireCSRF wraps a handler and enforces CSRF on state-changing methods.
func RequireCSRF(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
			sessID := SessionIDFromContext(r.Context())
			if sessID == "" || !ValidateCSRF(r, sessID, secret) {
				csrfReject(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// csrfReject responds to a CSRF failure. For HTMX requests it sends
// HX-Refresh to trigger a full page reload (which fetches a new token).
// For regular form POSTs it redirects back to the page via GET.
func csrfReject(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Refresh", "true")
		http.Error(w, "Forbidden — invalid CSRF token", http.StatusForbidden)
		return
	}
	// Redirect back so the user gets a fresh page with a valid token.
	target := r.Header.Get("Referer")
	if target == "" {
		target = r.URL.Path
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
