package web

import (
	"encoding/json"
	"errors"
	"iter"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"golang.org/x/crypto/bcrypt"
)

type settingsHandler struct{ handler }

// RedirectToProfile redirects /settings → /settings/profile.
func (h *settingsHandler) RedirectToProfile(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *settingsHandler) ShowProfile(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Profile", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsProfilePage(pd, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *settingsHandler) ShowSecurity(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Security", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsSecurityPage(pd, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *settingsHandler) ShowStorage(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Storage", h.p.CSRFSecret, h.p.Plugins)
	u := auth.UserFromContext(r.Context())

	// Mounts as iterator.
	var mounts iter.Seq2[mount.Mount, error]
	if h.p.Mounts != nil {
		mounts = h.p.Mounts.ListByUser(u.ID)
	}

	// Volumes: collect for <select> dropdown + volumeNames map.
	var volumes []volume.Volume
	if h.p.Access != nil {
		volumes, _ = seq.Collect(h.p.Access.ListAccessible(u.ID))
	} else if h.p.Volumes != nil {
		volumes, _ = seq.Collect(h.p.Volumes.List())
	}

	volumeNames := make(map[string]string, len(volumes))
	for _, v := range volumes {
		volumeNames[v.ID] = v.Name
	}

	// Owned volumes as iterator + has-bool.
	ownedFilter := seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
		return v.OwnerID != nil && *v.OwnerID == u.ID
	})
	var ownedVolumes iter.Seq2[volume.Volume, error]
	var hasOwnedVolumes bool
	var ownedErr error
	_, ownedVolumes, ownedErr = seq.Peek(ownedFilter)
	hasOwnedVolumes = !errors.Is(ownedErr, seq.ErrEmpty)
	if !hasOwnedVolumes {
		ownedVolumes = seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
			return v.OwnerID != nil && *v.OwnerID == u.ID
		})
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsStoragePage(pd, flashMsg, flashKind, mounts, volumes, volumeNames, ownedVolumes, hasOwnedVolumes).Render(r.Context(), w)
}

func (h *settingsHandler) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	displayName := r.FormValue("display_name")
	email := r.FormValue("email")
	if err := h.p.Users.Update(u.ID, user.UpdateOpts{DisplayName: &displayName, Email: &email}); err != nil {
		setFlash(w, "error", "Failed to update profile.")
	} else {
		setFlash(w, "success", "Profile updated.")
	}
	http.Redirect(w, r, "/settings/profile", http.StatusSeeOther)
}

func (h *settingsHandler) UpdatePassword(w http.ResponseWriter, r *http.Request) {
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

	if err := h.p.Users.UpdatePassword(u.ID, newPass); err != nil {
		setFlash(w, "error", "Failed to update password.")
		http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "Password changed.")
	http.Redirect(w, r, "/settings/security", http.StatusSeeOther)
}

func (h *settingsHandler) CreateMount(w http.ResponseWriter, r *http.Request) {
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
	if h.p.Access != nil {
		if ok, _ := h.p.Access.CanAccess(u.ID, volumeID); !ok {
			setFlash(w, "error", "You do not have access to that volume.")
			http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
			return
		}
	}

	_, err := h.p.Mounts.Create(u.ID, userPath, volumeID, volumeSubpath, false)
	if err != nil {
		setFlash(w, "error", "Failed to create mount: "+err.Error())
	} else {
		setFlash(w, "success", "Mount added.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *settingsHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
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

	_, err := h.p.Volumes.Create(name, "WebDAV volume", "webdav://"+name, u.Username, &u.ID, string(cfgJSON))
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
	} else {
		setFlash(w, "success", "Volume created.")
	}
	http.Redirect(w, r, "/settings/storage", http.StatusSeeOther)
}

func (h *settingsHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	vol, err := h.p.Volumes.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Only owner can delete their volume.
	if vol.OwnerID == nil || *vol.OwnerID != u.ID {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if err := h.p.Volumes.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (h *settingsHandler) DeleteMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")

	// Prevent deleting root mount.
	m, err := h.p.Mounts.GetByID(id)
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

	if err := h.p.Mounts.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
