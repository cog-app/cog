package web

import (
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
)

// mfaEnforcement returns middleware that redirects users to security settings
// if MFA is required by admin policy but the user hasn't set it up. It
// applies to browser sessions: a sync client signed in with an app password
// or Basic auth is answered by its own routes, not sent to an HTML page.
func mfaEnforcement(p *services.Providers) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFromContext(r.Context())
			if u == nil || u.MFAMethod != "" || u.IsAgent() || auth.SessionIDFromContext(r.Context()) == "" {
				next.ServeHTTP(w, r)
				return
			}

			// Only what setting MFA up needs is reachable without it: the
			// security page and its TOTP and passkey actions, and signing
			// out. The rest of the settings (export, app passwords, mounts)
			// wait like every other page.
			path := r.URL.Path
			if path == "/settings/security" || strings.HasPrefix(path, "/settings/totp/") ||
				strings.HasPrefix(path, "/settings/webauthn/") || strings.HasPrefix(path, "/logout") ||
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
