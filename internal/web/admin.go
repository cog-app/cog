package web

import (
	"log"
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
)

type adminHandler struct{ handler }

// --- Index ---

func (h *adminHandler) Index(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Admin", h.p.CSRFSecret, h.p.Plugins)
	adminIndexPage(pd).Render(r.Context(), w)
}

// --- Users ---

func (h *adminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Users", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminUsersPage(pd, h.p.Users.List(), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) NewUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New User", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	adminUserNewPage(pd, errMsg).Render(r.Context(), w)
}

func (h *adminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Failed to create user: "+err.Error())
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	// Set admin if checked.
	if r.FormValue("is_admin") == "1" {
		isAdmin := true
		if err := h.p.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &isAdmin}); err != nil {
			log.Printf("admin: set admin for %s: %v", u.ID, err)
		}
	}

	// Create root mount pointing to default volume.
	if h.p.Volumes != nil && h.p.Mounts != nil {
		if defVol, err := h.p.Volumes.GetByName("default"); err == nil {
			subpath := u.Username + "/files"
			if _, err := h.p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false); err != nil {
				log.Printf("admin: create mount for %s: %v", u.Username, err)
			}
		}
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *adminHandler) EditUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit User", h.p.CSRFSecret, h.p.Plugins)
	id := r.PathValue("id")
	u, err := h.p.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminUserEditPage(pd, u, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.p.Users.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}

	displayName := r.FormValue("display_name")
	isAdmin := r.FormValue("is_admin") == "1"
	if err := h.p.Users.Update(id, user.UpdateOpts{DisplayName: &displayName, IsAdmin: &isAdmin}); err != nil {
		setFlash(w, "error", "Failed to update user.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}

	setFlash(w, "success", "User updated.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *adminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.p.Users.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	newPass := r.FormValue("new_password")
	if newPass == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	if err := h.p.Users.UpdatePassword(id, newPass); err != nil {
		setFlash(w, "error", "Failed to reset password.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Password reset.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *adminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Delete mounts before user (no ON DELETE CASCADE).
	if h.p.Mounts != nil {
		if err := h.p.Mounts.DeleteByUser(id); err != nil {
			log.Printf("admin: delete mounts for user %s: %v", id, err)
		}
	}
	if err := h.p.Users.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return empty body — HTMX will remove the row.
	w.WriteHeader(http.StatusOK)
}

// --- Groups ---

func (h *adminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Groups", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminGroupsPage(pd, h.p.Groups.List(), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Group name is required.")
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	ownerID := r.FormValue("owner_id")
	g, err := h.p.Groups.Create(name, nil, nil)
	if err != nil {
		setFlash(w, "error", "Failed to create group: "+err.Error())
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	if ownerID != "" {
		if err := h.p.Groups.AddMember(g.ID, ownerID, "OWNER"); err != nil {
			log.Printf("admin: add owner %s to group %s: %v", ownerID, g.ID, err)
		}
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *adminHandler) EditGroupForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit Group", h.p.CSRFSecret, h.p.Plugins)
	id := r.PathValue("id")
	g, err := h.p.Groups.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	userNames, _ := seq.ToMap(h.p.Users.List(), func(u user.User) string { return u.ID }, func(u user.User) string { return u.Username })
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminGroupEditPage(pd, g, h.p.Groups.ListMembers(id), userNames, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.FormValue("name")

	if err := h.p.Groups.Update(id, name); err != nil {
		http.NotFound(w, r)
		return
	}

	setFlash(w, "success", "Group updated.")
	http.Redirect(w, r, "/admin/groups/"+id, http.StatusSeeOther)
}

func (h *adminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.p.Groups.Delete(id); err != nil {
		setFlash(w, "error", "Failed to delete group: "+err.Error())
		http.Redirect(w, r, "/admin/groups/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Group deleted.")
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *adminHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberID := r.FormValue("member_id")
	memberType := r.FormValue("member_type")
	if memberType == "" {
		memberType = "USER"
	}
	if err := h.p.Groups.AddMember(groupID, memberID, memberType); err != nil {
		setFlash(w, "error", "Failed to add member: "+err.Error())
	}
	http.Redirect(w, r, "/admin/groups/"+groupID, http.StatusSeeOther)
}

func (h *adminHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberID := r.PathValue("member_id")
	if err := h.p.Groups.RemoveMember(groupID, memberID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- Plugins ---

// PluginInfo holds display data for the admin plugins page.
type PluginInfo struct {
	ID        string
	Enabled   bool
	Protected bool
}

func (h *adminHandler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Plugins", h.p.CSRFSecret, h.p.Plugins)
	var plugins []PluginInfo
	if h.p.Plugins != nil {
		for _, id := range h.p.Plugins.PluginIDs() {
			plugins = append(plugins, PluginInfo{
				ID:        id,
				Enabled:   h.p.Plugins.IsEnabled(id),
				Protected: h.p.Plugins.IsPluginProtected(id),
			})
		}
	}
	adminPluginsPage(pd, plugins).Render(r.Context(), w)
}

func (h *adminHandler) TogglePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.p.Plugins == nil || h.p.Plugins.IsPluginProtected(id) {
		http.Error(w, "cannot toggle this plugin", http.StatusBadRequest)
		return
	}
	enabled := !h.p.Plugins.IsEnabled(id)
	h.p.Plugins.SetEnabled(id, enabled)
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// --- Settings ---

func (h *adminHandler) ShowSettings(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Settings", h.p.CSRFSecret, h.p.Plugins)
	pubReg := h.p.Settings != nil && h.p.Settings.GetBool(settings.KeyPublicRegistration)
	expiryHours := settings.DefaultInviteExpiryHours
	if h.p.Settings != nil {
		expiryHours = h.p.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminSettingsPage(pd, pubReg, strconv.Itoa(expiryHours), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	pubReg := r.FormValue("public_registration") == "1"
	expiryStr := r.FormValue("invite_expiry_hours")
	expiryHours, err := strconv.Atoi(expiryStr)
	if err != nil || expiryHours < 1 {
		expiryHours = settings.DefaultInviteExpiryHours
	}
	if h.p.Settings != nil {
		if err := h.p.Settings.SetBool(settings.KeyPublicRegistration, pubReg); err != nil {
			log.Printf("admin: save public_registration: %v", err)
		}
		if err := h.p.Settings.SetInt(settings.KeyInviteExpiryHours, expiryHours); err != nil {
			log.Printf("admin: save invite_expiry_hours: %v", err)
		}
	}
	setFlash(w, "success", "Settings saved.")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
