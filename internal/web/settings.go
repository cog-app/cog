package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/user"
	"golang.org/x/crypto/bcrypt"
)

type SettingsHandler struct {
	Users  *user.Store
	Secret []byte
}

func (h *SettingsHandler) Show(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Settings", h.Secret)
	settingsPage(pd, "", "").Render(r.Context(), w)
}

func (h *SettingsHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	displayName := r.FormValue("display_name")
	if err := h.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName}); err != nil {
		pd := pageData(r, "Settings", h.Secret)
		settingsPage(pd, "Failed to update profile.", "error").Render(r.Context(), w)
		return
	}
	// Refresh user in context for the response.
	u.DisplayName = displayName
	pd := pageData(r, "Settings", h.Secret)
	pd.User = u
	settingsPage(pd, "Profile updated.", "success").Render(r.Context(), w)
}

func (h *SettingsHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	current := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	pd := pageData(r, "Settings", h.Secret)

	if newPass != confirm {
		settingsPage(pd, "New passwords do not match.", "error").Render(r.Context(), w)
		return
	}
	if newPass == "" {
		settingsPage(pd, "New password cannot be empty.", "error").Render(r.Context(), w)
		return
	}

	// Verify current password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		settingsPage(pd, "Current password is incorrect.", "error").Render(r.Context(), w)
		return
	}

	if err := h.Users.UpdatePassword(u.ID, newPass); err != nil {
		settingsPage(pd, "Failed to update password.", "error").Render(r.Context(), w)
		return
	}

	settingsPage(pd, "Password changed.", "success").Render(r.Context(), w)
}
