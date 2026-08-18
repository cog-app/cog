package web

import (
	"log/slog"
	"net/http"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// cookieCfg is set once by New() at startup and read by flash helpers and any
// other package-level cookie writer in this package. Not safe to mutate after
// startup.
var cookieCfg cogplugin.CookieConfig

// Flash holds a one-time message displayed after a redirect. The cookie itself
// is written and read by plugin.CookieConfig, which is also what the plugin
// shell and the CSRF middleware use, so a message set anywhere renders
// everywhere.
type Flash = cogplugin.Flash

// setFlash stores a flash message in a cookie for the next GET request.
func setFlash(w http.ResponseWriter, kind, message string, errs ...error) {
	for err := range errs {
		slog.Error(message, "kind", kind, "err", err)
	}
	cookieCfg.SetFlash(w, Flash{Kind: kind, Message: message})
}

// setFlashURL stores a flash message with a copyable URL.
func setFlashURL(w http.ResponseWriter, kind, message, linkURL string) {
	cookieCfg.SetFlash(w, Flash{Kind: kind, Message: message, URL: linkURL})
}

// getFlash reads and clears the flash cookie. Returns nil if no flash is set.
func getFlash(w http.ResponseWriter, r *http.Request) *Flash {
	return cookieCfg.TakeFlash(w, r)
}
