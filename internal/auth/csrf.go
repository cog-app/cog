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

// RequireCSRF wraps a handler and enforces CSRF on state-changing methods.
func RequireCSRF(secret []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
			sessID := SessionIDFromContext(r.Context())
			if sessID == "" || !ValidateCSRF(r, sessID, secret) {
				http.Error(w, "Forbidden — invalid CSRF token", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
