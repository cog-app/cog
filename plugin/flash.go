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

// FlashHeader is how a plugin hands a message to the host. A plugin does not
// write the cookie itself: an out-of-process one is not told the deployment's
// cookie settings, so its cookie would come out without Secure on an HTTPS
// site. The host moves the header onto the cookie on the way out, the same way
// it turns X-Page-Title into the page title.
const FlashHeader = "X-Flash"

// SetFlash asks the host to show a message on whichever page this request
// redirects to. Set it before writing the redirect.
func SetFlash(w http.ResponseWriter, f Flash) {
	if v := encodeFlash(f); v != "" {
		w.Header().Set(FlashHeader, v)
	}
}

// SetFlash stores a message for the next GET request to render.
func (c CookieConfig) SetFlash(w http.ResponseWriter, f Flash) {
	if v := encodeFlash(f); v != "" {
		c.setFlashValue(w, v)
	}
}

// TakeFlashHeader moves a plugin's FlashHeader onto the flash cookie and
// removes the header. The header value is already the cookie's encoding, so
// nothing is decoded and re-encoded in between.
func (c CookieConfig) TakeFlashHeader(w http.ResponseWriter) {
	v := w.Header().Get(FlashHeader)
	if v == "" {
		return
	}
	w.Header().Del(FlashHeader)
	c.setFlashValue(w, v)
}

func (c CookieConfig) setFlashValue(w http.ResponseWriter, value string) {
	c.Set(w, &http.Cookie{
		Name:   FlashCookieName,
		Value:  value,
		MaxAge: flashMaxAge,
	})
}

func encodeFlash(f Flash) string {
	data, err := json.Marshal(f)
	if err != nil {
		return ""
	}
	return url.QueryEscape(string(data))
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
