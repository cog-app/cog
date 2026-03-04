package web

import (
	"encoding/json"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"golang.org/x/crypto/bcrypt"
)

type SettingsHandler struct {
	Users    *user.Store
	Volumes  *volume.Store
	Mounts   *mount.Store
	Access   *volume.Access
	Registry *plugin.Registry
	Secret   []byte
}

// RedirectToProfile redirects /settings → /settings/profile.
func (h *SettingsHandler) RedirectToProfile(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *SettingsHandler) ShowProfile(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Profile", h.Secret, h.Registry)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsProfilePage(pd, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *SettingsHandler) ShowSecurity(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Security", h.Secret, h.Registry)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsSecurityPage(pd, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *SettingsHandler) ShowStorage(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Storage", h.Secret, h.Registry)
	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsStoragePage(pd, flashMsg, flashKind, mounts, volumes, ownedVolumes).Render(r.Context(), w)
}

// userMountsAndVolumes loads the current user's mounts, accessible volumes, and owned volumes.
func (h *SettingsHandler) userMountsAndVolumes(r *http.Request) ([]mount.Mount, []volume.Volume, []volume.Volume) {
	u := auth.UserFromContext(r.Context())
	var mounts []mount.Mount
	var volumes []volume.Volume
	var ownedVolumes []volume.Volume
	if h.Mounts != nil {
		mounts, _ = h.Mounts.ListByUser(u.ID)
	}
	if h.Access != nil {
		volumes, _ = h.Access.ListAccessible(u.ID)
	} else if h.Volumes != nil {
		volumes, _ = h.Volumes.List()
	}
	// Owned volumes: those where OwnerID matches.
	for _, v := range volumes {
		if v.OwnerID != nil && *v.OwnerID == u.ID {
			ownedVolumes = append(ownedVolumes, v)
		}
	}
	return mounts, volumes, ownedVolumes
}

func (h *SettingsHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	displayName := r.FormValue("display_name")
	email := r.FormValue("email")
	if err := h.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName, Email: &email}); err != nil {
		setFlash(w, "error", "Failed to update profile.")
	} else {
		setFlash(w, "success", "Profile updated.")
	}
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *SettingsHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	current := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	if newPass != confirm {
		setFlash(w, "error", "New passwords do not match.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}
	if newPass == "" {
		setFlash(w, "error", "New password cannot be empty.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	// Verify current password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		setFlash(w, "error", "Current password is incorrect.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	if err := h.Users.UpdatePassword(u.ID, newPass); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Password changed.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

func (h *SettingsHandler) CreateMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volumeID := r.FormValue("volume_id")
	userPath := r.FormValue("user_path")
	volumeSubpath := r.FormValue("volume_subpath")

	if volumeID == "" || userPath == "" {
		setFlash(w, "error", "Volume and path are required.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}

	// Access check on the volume.
	if h.Access != nil {
		if ok, _ := h.Access.CanAccess(u.ID, volumeID); !ok {
			setFlash(w, "error", "You do not have access to that volume.")
			http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
			return
		}
	}

	_, err := h.Mounts.Create(u.ID, userPath, volumeID, volumeSubpath, false)
	if err != nil {
		setFlash(w, "error", "Failed to create mount: "+err.Error())
	} else {
		setFlash(w, "success", "Mount added.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *SettingsHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	remoteURL := r.FormValue("remote_url")
	authType := r.FormValue("auth_type")
	username := r.FormValue("webdav_username")
	password := r.FormValue("webdav_password")
	token := r.FormValue("webdav_token")

	if name == "" || remoteURL == "" {
		setFlash(w, "error", "Name and remote URL are required.")
		http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
		return
	}

	cfg := volume.WebDAVConfig{
		URL:      remoteURL,
		AuthType: authType,
		Username: username,
		Password: password,
		Token:    token,
	}
	cfgJSON, _ := json.Marshal(cfg)

	_, err := h.Volumes.Create(name, "WebDAV volume", "webdav://"+name, u.Username, &u.ID, string(cfgJSON))
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
	} else {
		setFlash(w, "success", "Volume created.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *SettingsHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	vol, err := h.Volumes.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Only owner can delete their volume.
	if vol.OwnerID == nil || *vol.OwnerID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.Volumes.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *SettingsHandler) DeleteMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	// Prevent deleting root mount.
	m, err := h.Mounts.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if m.UserID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if m.UserPath == "/" {
		http.Error(w, "cannot delete root mount", http.StatusBadRequest)
		return
	}

	if err := h.Mounts.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
