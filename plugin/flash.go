package plugin

import (
	"encoding/json"
	"log/slog"
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
	// Form carries what the user typed back to the form a failed POST came
	// from, so an error does not also cost them their input. Never put a
	// password or other secret here: the cookie is readable by the browser.
	Form map[string]string `json:"f,omitempty"`
}

// Value returns the form value the flash carried for name, or "". It is safe
// to call on a nil flash.
func (f *Flash) Value(name string) string {
	if f == nil {
		return ""
	}
	return f.Form[name]
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
		slog.Error("encode flash", "err", err)
		return ""
	}
	return url.QueryEscape(string(data))
}

// TakeFlash reads the pending message and clears it, so a reload does not show
// it again. It returns nil when there is none.
func (c CookieConfig) TakeFlash(w http.ResponseWriter, r *http.Request) *Flash {
	if _, err := r.Cookie(FlashCookieName); err != nil {
		return nil
	}
	c.Clear(w, FlashCookieName)
	return RequestFlash(r)
}

// RequestFlash decodes the message pending on r without clearing it, or
// returns nil when there is none. A page handler uses it to refill a form from
// Flash.Form; the host's page shell is what renders the message and clears the
// cookie, so a plugin never has to.
func RequestFlash(r *http.Request) *Flash {
	cookie, err := r.Cookie(FlashCookieName)
	if err != nil {
		return nil
	}
	val, err := url.QueryUnescape(cookie.Value)
	if err != nil {
		slog.Warn("decode flash cookie", "err", err)
		return nil
	}
	var f Flash
	if err := json.Unmarshal([]byte(val), &f); err != nil {
		slog.Warn("decode flash cookie", "err", err)
		return nil
	}
	return &f
}
