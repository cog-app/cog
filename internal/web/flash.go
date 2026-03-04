package web

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
)

const flashCookieName = "cog_flash"

// Flash holds a one-time message displayed after a redirect.
type Flash struct {
	Kind    string `json:"k"`
	Message string `json:"m"`
	URL     string `json:"u,omitempty"`
}

// setFlash stores a flash message in a cookie for the next GET request.
func setFlash(w http.ResponseWriter, kind, message string, errs ...error) {
	for err := range errs {
		slog.Error(message, "kind", kind, "err", err)
	}
	writeFlash(w, Flash{Kind: kind, Message: message})
}

// setFlashURL stores a flash message with a copyable URL.
func setFlashURL(w http.ResponseWriter, kind, message, linkURL string) {
	writeFlash(w, Flash{Kind: kind, Message: message, URL: linkURL})
}

func writeFlash(w http.ResponseWriter, f Flash) {
	data, _ := json.Marshal(f)
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    url.QueryEscape(string(data)),
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// getFlash reads and clears the flash cookie. Returns nil if no flash is set.
func getFlash(w http.ResponseWriter, r *http.Request) *Flash {
	cookie, err := r.Cookie(flashCookieName)
	if err != nil {
		return nil
	}
	// Clear immediately.
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	val, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		return nil
	}
	var f Flash
	if err := json.Unmarshal([]byte(val), &f); err != nil {
		return nil
	}
	return &f
}
