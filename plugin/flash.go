package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
)

// FlashCookieName is the cookie a one-time message travels in between the POST
// that produced it and the GET that renders it.
const FlashCookieName = "cog_flash"

// flashMaxAge bounds how long an undelivered message can sit in the browser. A
// flash belongs to the redirect that follows it; a minute is generous for that
// and short enough that a message never surfaces on some unrelated page later.
const flashMaxAge = 60

// Flash is a one-time message displayed after a redirect. The field names are
// short because the value is JSON in a cookie.
type Flash struct {
	Kind    string `json:"k"` // "success" or "error"; the alert's CSS variant
	Message string `json:"m"`
	URL     string `json:"u,omitempty"` // rendered as a copyable link
}

// SetFlash stores a message for the next GET request to render.
func (c CookieConfig) SetFlash(w http.ResponseWriter, f Flash) {
	data, err := json.Marshal(f)
	if err != nil {
		return
	}
	c.Set(w, &http.Cookie{
		Name:   FlashCookieName,
		Value:  url.QueryEscape(string(data)),
		MaxAge: flashMaxAge,
	})
}

// TakeFlash reads the pending message and clears it, so a reload does not show
// it again. It returns nil when there is none.
func (c CookieConfig) TakeFlash(w http.ResponseWriter, r *http.Request) *Flash {
	cookie, err := r.Cookie(FlashCookieName)
	if err != nil {
		return nil
	}
	c.Clear(w, FlashCookieName)
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
