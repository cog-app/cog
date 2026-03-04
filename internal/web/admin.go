package web

import (
	"net/http"
	"strconv"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

type AdminHandler struct {
	Users    *user.Store
	Groups   *group.Store
	Volumes  *volume.Store
	Mounts   *mount.Store
	Plugins  *plugin.Registry
	Settings *settings.Store
	Secret   []byte
}

// --- Index ---

func (h *AdminHandler) Index(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Admin", h.Secret, h.Plugins)
	adminIndexPage(pd).Render(r.Context(), w)
}

// --- Users ---

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Users", h.Secret, h.Plugins)
	users, _ := h.Users.List()
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminUsersPage(pd, users, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminHandler) NewUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New User", h.Secret, h.Plugins)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	adminUserNewPage(pd, errMsg).Render(r.Context(), w)
}

func (h *AdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	u, err := h.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Failed to create user: "+err.Error())
		http.Redirect(w, r, "/admin/users/new", http.StatusSeeOther)
		return
	}

	// Set admin if checked.
	if r.FormValue("is_admin") == "1" {
		isAdmin := true
		h.Users.Update(u.ID, user.UpdateOpts{IsAdmin: &isAdmin})
	}

	// Create root mount pointing to default volume.
	if h.Volumes != nil && h.Mounts != nil {
		if defVol, err := h.Volumes.GetByName("default"); err == nil {
			subpath := u.Username + "/files"
			h.Mounts.Create(u.ID, "/", defVol.ID, subpath, false)
		}
	}

	http.Redirect(w, r, "/admin/users", http.StatusSeeOther)
}

func (h *AdminHandler) EditUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit User", h.Secret, h.Plugins)
	id := r.PathValue("id")
	u, err := h.Users.GetByID(id)
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

func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Users.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}

	displayName := r.FormValue("display_name")
	isAdmin := r.FormValue("is_admin") == "1"
	h.Users.Update(id, user.UpdateOpts{DisplayName: &displayName, IsAdmin: &isAdmin})

	setFlash(w, "success", "User updated.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *AdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Users.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}
	newPass := r.FormValue("new_password")
	if newPass == "" {
		setFlash(w, "error", "Password cannot be empty.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	if err := h.Users.UpdatePassword(id, newPass); err != nil {
		setFlash(w, "error", "Failed to reset password.")
		http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Password reset.")
	http.Redirect(w, r, "/admin/users/"+id, http.StatusSeeOther)
}

func (h *AdminHandler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Delete mounts before user (no ON DELETE CASCADE).
	if h.Mounts != nil {
		h.Mounts.DeleteByUser(id)
	}
	if err := h.Users.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Return empty body — HTMX will remove the row.
	w.WriteHeader(http.StatusOK)
}

// --- Groups ---

func (h *AdminHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Groups", h.Secret, h.Plugins)
	groups, _ := h.Groups.List()
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminGroupsPage(pd, groups, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Group name is required.")
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	ownerID := r.FormValue("owner_id")
	g, err := h.Groups.Create(name, nil, nil)
	if err != nil {
		setFlash(w, "error", "Failed to create group: "+err.Error())
		http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
		return
	}
	if ownerID != "" {
		h.Groups.AddMember(g.ID, ownerID, "OWNER")
	}
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *AdminHandler) EditGroupForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit Group", h.Secret, h.Plugins)
	id := r.PathValue("id")
	g, err := h.Groups.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, _ := h.Groups.ListMembers(id)
	users, _ := h.Users.List()
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminGroupEditPage(pd, g, members, users, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.FormValue("name")

	if err := h.Groups.Update(id, name); err != nil {
		http.NotFound(w, r)
		return
	}

	setFlash(w, "success", "Group updated.")
	http.Redirect(w, r, "/admin/groups/"+id, http.StatusSeeOther)
}

func (h *AdminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Groups.Delete(id); err != nil {
		setFlash(w, "error", "Failed to delete group: "+err.Error())
		http.Redirect(w, r, "/admin/groups/"+id, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Group deleted.")
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *AdminHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberID := r.FormValue("member_id")
	memberType := r.FormValue("member_type")
	if memberType == "" {
		memberType = "USER"
	}
	h.Groups.AddMember(groupID, memberID, memberType)
	http.Redirect(w, r, "/admin/groups/"+groupID, http.StatusSeeOther)
}

func (h *AdminHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	groupID := r.PathValue("id")
	memberID := r.PathValue("member_id")
	h.Groups.RemoveMember(groupID, memberID)
	w.WriteHeader(http.StatusOK)
}

// --- Plugins ---

// PluginInfo holds display data for the admin plugins page.
type PluginInfo struct {
	ID        string
	Enabled   bool
	Protected bool
}

func (h *AdminHandler) ListPlugins(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Plugins", h.Secret, h.Plugins)
	var plugins []PluginInfo
	if h.Plugins != nil {
		for _, id := range h.Plugins.PluginIDs() {
			plugins = append(plugins, PluginInfo{
				ID:        id,
				Enabled:   h.Plugins.IsEnabled(id),
				Protected: h.Plugins.IsPluginProtected(id),
			})
		}
	}
	adminPluginsPage(pd, plugins).Render(r.Context(), w)
}

func (h *AdminHandler) TogglePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.Plugins == nil || h.Plugins.IsPluginProtected(id) {
		http.Error(w, "cannot toggle this plugin", http.StatusBadRequest)
		return
	}
	enabled := !h.Plugins.IsEnabled(id)
	h.Plugins.SetEnabled(id, enabled)
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}

// --- Settings ---

func (h *AdminHandler) ShowSettings(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Settings", h.Secret, h.Plugins)
	pubReg := h.Settings != nil && h.Settings.GetBool(settings.KeyPublicRegistration)
	expiryHours := settings.DefaultInviteExpiryHours
	if h.Settings != nil {
		expiryHours = h.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminSettingsPage(pd, pubReg, strconv.Itoa(expiryHours), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	pubReg := r.FormValue("public_registration") == "1"
	expiryStr := r.FormValue("invite_expiry_hours")
	expiryHours, err := strconv.Atoi(expiryStr)
	if err != nil || expiryHours < 1 {
		expiryHours = settings.DefaultInviteExpiryHours
	}
	if h.Settings != nil {
		h.Settings.SetBool(settings.KeyPublicRegistration, pubReg)
		h.Settings.SetInt(settings.KeyInviteExpiryHours, expiryHours)
	}
	setFlash(w, "success", "Settings saved.")
	http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
}
