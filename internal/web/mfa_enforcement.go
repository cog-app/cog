package web

import (
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
)

// mfaEnforcement returns middleware that redirects users to security settings
// if MFA is required by admin policy but the user hasn't set it up.
// Only applies to authenticated web requests (not API/WebDAV).
func mfaEnforcement(p *services.Providers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFromContext(r.Context())
			if u == nil || u.MFAMethod != "" {
				next.ServeHTTP(w, r)
				return
			}

			// Don't enforce on paths that the user needs to access to set up MFA.
			path := r.URL.Path
			if strings.HasPrefix(path, "/settings/") || strings.HasPrefix(path, "/logout") ||
				strings.HasPrefix(path, "/static/") || strings.HasPrefix(path, "/avatar/") {
				next.ServeHTTP(w, r)
				return
			}

			// Check if MFA is required.
			if p.Settings == nil {
				next.ServeHTTP(w, r)
				return
			}
			required, err := p.Settings.Get(settings.KeyMFARequired)
			if err != nil {
				// Fail closed: without the policy we can't tell whether
				// this user may proceed without MFA.
				p.Log.Error("mfa enforcement: read policy", "err", err)
				http.Error(w, "Internal error", http.StatusInternalServerError)
				return
			}
			if required == "" {
				next.ServeHTTP(w, r)
				return
			}

			enforce := false
			switch required {
			case "all":
				enforce = true
			case "admins":
				enforce = u.IsAdmin
			}

			if enforce {
				setFlash(w, "error", "Your administrator requires multi-factor authentication. Please set up MFA to continue.")
				http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
