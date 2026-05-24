package plugin

import (
	"net/http"
	"strings"
)

// CookieConfig controls cookie attributes that depend on deployment context
// (HTTP vs HTTPS). A zero value sets Secure=false (HTTP dev only).
type CookieConfig struct {
	Secure bool
}

// CookieConfigFromBaseURL returns config with Secure=true when baseURL starts with https://.
func CookieConfigFromBaseURL(baseURL string) CookieConfig {
	return CookieConfig{Secure: strings.HasPrefix(baseURL, "https://")}
}

// Set writes a cookie with HttpOnly=true and Secure derived from config.
// Path defaults to "/" and SameSite defaults to Lax. Caller may override either
// by setting the field on ck before calling.
func (c CookieConfig) Set(w http.ResponseWriter, ck *http.Cookie) {
	if ck.Path == "" {
		ck.Path = "/"
	}
	ck.Secure = c.Secure
	ck.HttpOnly = true
	if ck.SameSite == 0 {
		ck.SameSite = http.SameSiteLaxMode
	}
	http.SetCookie(w, ck)
}

// Clear writes a deletion cookie (MaxAge=-1) for name.
func (c CookieConfig) Clear(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Secure,
	})
}
