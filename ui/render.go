package ui

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
)

// Render writes c to w as the response to r. A render that fails part-way
// has already sent the status and part of the body, so there is nothing left
// to tell the client; the error is logged instead of being dropped.
func Render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	if err := c.Render(r.Context(), w); err != nil {
		slog.ErrorContext(r.Context(), "render page", "path", r.URL.Path, "err", err)
	}
}
