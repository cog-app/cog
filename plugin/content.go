package plugin

import (
	"mime"
	"net/http"
)

// UntrustedContent sets the headers for serving bytes a user supplied — an
// uploaded file, an archive entry — on the server's own origin. Without them
// an HTML or SVG file opened in the browser runs its scripts as the site,
// with the session of whoever opened it: the owner of a shared volume, or an
// admin following a link.
//
// The content runs in a sandbox (a unique origin, no scripts) and the
// browser keeps to the declared type instead of sniffing one. PDFs are left
// out of the sandbox: browsers refuse to render one inside it, and a PDF's
// scripts run in the viewer, not on the page's origin.
func UntrustedContent(w http.ResponseWriter, contentType string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	if mt, _, err := mime.ParseMediaType(contentType); err == nil && mt == "application/pdf" {
		return
	}
	h.Set("Content-Security-Policy", "sandbox")
}
