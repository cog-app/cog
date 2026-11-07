package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// blockedSignIn is an address or account the server is refusing or slowing
// for failed attempts, as Admin › Users lists it.
type blockedSignIn struct {
	Kind   string // "Address", "Account"
	Name   string // "203.0.113.7", "alice"
	Effect string // what the block does
	Count  int
	Last   time.Time
}

// blockedSignIns lists what the sign-in and login-flow limiters are
// limiting now.
func (h *adminHandler) blockedSignIns() ([]blockedSignIn, error) {
	var out []blockedSignIn
	if h.p.RateLimiter != nil {
		over, err := h.p.RateLimiter.Over()
		if err != nil {
			return nil, fmt.Errorf("list sign-in limits: %w", err)
		}
		for _, o := range over {
			b := blockedSignIn{Count: o.Attempts, Last: o.Last}
			switch {
			case strings.HasPrefix(o.Key, "user:"):
				b.Kind, b.Name, b.Effect = "Account", strings.TrimPrefix(o.Key, "user:"), "Password sign-ins slowed"
			case strings.HasPrefix(o.Key, "share:"):
				b.Kind, b.Name, b.Effect = "Share link", strings.TrimPrefix(o.Key, "share:"), "Password attempts slowed"
			case strings.HasPrefix(o.Key, "mfa:"):
				id := strings.TrimPrefix(o.Key, "mfa:")
				u, err := h.p.Users.GetByID(id)
				switch {
				case errors.Is(err, user.ErrNotFound):
					b.Kind, b.Name = "Two-factor codes, deleted account", id
				case err != nil:
					return nil, fmt.Errorf("look up %s: %w", o.Key, err)
				default:
					b.Kind, b.Name = "Two-factor codes", u.Username
				}
				b.Effect = "Codes slowed"
			default:
				b.Kind, b.Name, b.Effect = "Address", o.Key, "Password sign-ins refused"
			}
			out = append(out, b)
		}
	}
	if h.p.FlowLimiter != nil {
		over, err := h.p.FlowLimiter.Over()
		if err != nil {
			return nil, fmt.Errorf("list login-flow limits: %w", err)
		}
		for _, o := range over {
			out = append(out, blockedSignIn{Kind: "Address", Name: o.Key, Effect: "App sign-in polls refused", Count: o.Attempts, Last: o.Last})
		}
	}
	return out, nil
}

// ClearSignInLimits forgets every failed attempt the server counts, so no
// address or account is refused or slowed: what an admin locked out on a
// phone needs, with no shell to reach the database from.
func (h *adminHandler) ClearSignInLimits(w http.ResponseWriter, r *http.Request) {
	for _, l := range []*ratelimit.Limiter{h.p.RateLimiter, h.p.FlowLimiter} {
		if l == nil {
			continue
		}
		if err := l.Clear(); err != nil {
			setFlash(w, "error", "The sign-in blocks could not be cleared.", err)
			http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
			return
		}
	}
	h.p.Log.Info("sign-in blocks cleared", "admin", auth.UserFromContext(r.Context()).Username)
	setFlash(w, "success", "Sign-in blocks cleared.")
	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}
