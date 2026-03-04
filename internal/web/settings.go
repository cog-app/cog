package web

import (
	"encoding/json"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"golang.org/x/crypto/bcrypt"
)

type SettingsHandler struct {
	Users   *user.Store
	Volumes *volume.Store
	Mounts  *mount.Store
	Access  *volume.Access
	Secret  []byte
}

func (h *SettingsHandler) Show(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Settings", h.Secret)
	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
	settingsPage(pd, "", "", mounts, volumes, ownedVolumes).Render(r.Context(), w)
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
	if err := h.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName}); err != nil {
		pd := pageData(r, "Settings", h.Secret)
		mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
		settingsPage(pd, "Failed to update profile.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}
	// Refresh user in context for the response.
	u.DisplayName = displayName
	pd := pageData(r, "Settings", h.Secret)
	pd.User = u
	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
	settingsPage(pd, "Profile updated.", "success", mounts, volumes, ownedVolumes).Render(r.Context(), w)
}

func (h *SettingsHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	current := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")

	pd := pageData(r, "Settings", h.Secret)

	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)

	if newPass != confirm {
		settingsPage(pd, "New passwords do not match.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}
	if newPass == "" {
		settingsPage(pd, "New password cannot be empty.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	// Verify current password.
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(current)); err != nil {
		settingsPage(pd, "Current password is incorrect.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	if err := h.Users.UpdatePassword(u.ID, newPass); err != nil {
		settingsPage(pd, "Failed to update password.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	settingsPage(pd, "Password changed.", "success", mounts, volumes, ownedVolumes).Render(r.Context(), w)
}

func (h *SettingsHandler) CreateMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volumeID := r.FormValue("volume_id")
	userPath := r.FormValue("user_path")
	volumeSubpath := r.FormValue("volume_subpath")

	pd := pageData(r, "Settings", h.Secret)

	if volumeID == "" || userPath == "" {
		mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
		settingsPage(pd, "Volume and path are required.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	// Access check on the volume.
	if h.Access != nil {
		if ok, _ := h.Access.CanAccess(u.ID, volumeID); !ok {
			mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
			settingsPage(pd, "You do not have access to that volume.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
			return
		}
	}

	_, err := h.Mounts.Create(u.ID, userPath, volumeID, volumeSubpath, false)
	if err != nil {
		mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
		settingsPage(pd, "Failed to create mount: "+err.Error(), "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
	settingsPage(pd, "Mount added.", "success", mounts, volumes, ownedVolumes).Render(r.Context(), w)
}

func (h *SettingsHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	remoteURL := r.FormValue("remote_url")
	authType := r.FormValue("auth_type")
	username := r.FormValue("webdav_username")
	password := r.FormValue("webdav_password")
	token := r.FormValue("webdav_token")

	pd := pageData(r, "Settings", h.Secret)

	if name == "" || remoteURL == "" {
		mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
		settingsPage(pd, "Name and remote URL are required.", "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
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
		mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
		settingsPage(pd, "Failed to create volume: "+err.Error(), "error", mounts, volumes, ownedVolumes).Render(r.Context(), w)
		return
	}

	mounts, volumes, ownedVolumes := h.userMountsAndVolumes(r)
	settingsPage(pd, "Volume created.", "success", mounts, volumes, ownedVolumes).Render(r.Context(), w)
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
