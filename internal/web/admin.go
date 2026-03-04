package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

type AdminHandler struct {
	Users    *user.Store
	Groups   *group.Store
	Volumes  *volume.Store
	Mounts   *mount.Store
	Plugins  *plugin.Registry
	Secret   []byte
}

// --- Index ---

func (h *AdminHandler) Index(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Admin", h.Secret)
	adminIndexPage(pd).Render(r.Context(), w)
}

// --- Users ---

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Users", h.Secret)
	users, _ := h.Users.List()
	adminUsersPage(pd, users, "", "").Render(r.Context(), w)
}

func (h *AdminHandler) NewUserForm(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "New User", h.Secret)
	adminUserNewPage(pd, "").Render(r.Context(), w)
}

func (h *AdminHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "New User", h.Secret)
	username := r.FormValue("username")
	password := r.FormValue("password")

	if username == "" || password == "" {
		adminUserNewPage(pd, "Username and password are required.").Render(r.Context(), w)
		return
	}

	u, err := h.Users.Create(username, password)
	if err != nil {
		adminUserNewPage(pd, "Failed to create user: "+err.Error()).Render(r.Context(), w)
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
	pd := pageData(r, "Edit User", h.Secret)
	id := r.PathValue("id")
	u, err := h.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	adminUserEditPage(pd, u, "", "").Render(r.Context(), w)
}

func (h *AdminHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	displayName := r.FormValue("display_name")
	isAdmin := r.FormValue("is_admin") == "1"
	h.Users.Update(id, user.UpdateOpts{DisplayName: &displayName, IsAdmin: &isAdmin})

	// Refresh
	u, _ = h.Users.GetByID(id)
	pd := pageData(r, "Edit User", h.Secret)
	adminUserEditPage(pd, u, "User updated.", "success").Render(r.Context(), w)
}

func (h *AdminHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	u, err := h.Users.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newPass := r.FormValue("new_password")
	pd := pageData(r, "Edit User", h.Secret)
	if newPass == "" {
		adminUserEditPage(pd, u, "Password cannot be empty.", "error").Render(r.Context(), w)
		return
	}
	if err := h.Users.UpdatePassword(id, newPass); err != nil {
		adminUserEditPage(pd, u, "Failed to reset password.", "error").Render(r.Context(), w)
		return
	}
	adminUserEditPage(pd, u, "Password reset.", "success").Render(r.Context(), w)
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
	pd := pageData(r, "Groups", h.Secret)
	groups, _ := h.Groups.List()
	adminGroupsPage(pd, groups, "", "").Render(r.Context(), w)
}

func (h *AdminHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		pd := pageData(r, "Groups", h.Secret)
		groups, _ := h.Groups.List()
		adminGroupsPage(pd, groups, "Group name is required.", "error").Render(r.Context(), w)
		return
	}
	h.Groups.Create(name, nil)
	http.Redirect(w, r, "/admin/groups", http.StatusSeeOther)
}

func (h *AdminHandler) EditGroupForm(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Edit Group", h.Secret)
	id := r.PathValue("id")
	g, err := h.Groups.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	members, _ := h.Groups.ListMembers(id)
	users, _ := h.Users.List()
	adminGroupEditPage(pd, g, members, users, "", "").Render(r.Context(), w)
}

func (h *AdminHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	name := r.FormValue("name")
	if err := h.Groups.Update(id, name); err != nil {
		http.NotFound(w, r)
		return
	}
	g, _ := h.Groups.Get(id)
	members, _ := h.Groups.ListMembers(id)
	users, _ := h.Users.List()
	pd := pageData(r, "Edit Group", h.Secret)
	adminGroupEditPage(pd, g, members, users, "Group updated.", "success").Render(r.Context(), w)
}

func (h *AdminHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Groups.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
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
	pd := pageData(r, "Plugins", h.Secret)
	var plugins []PluginInfo
	if h.Plugins != nil {
		for _, id := range h.Plugins.PluginIDs() {
			plugins = append(plugins, PluginInfo{
				ID:        id,
				Enabled:   h.Plugins.IsEnabled(id),
				Protected: h.Plugins.IsProtected(id),
			})
		}
	}
	adminPluginsPage(pd, plugins).Render(r.Context(), w)
}

func (h *AdminHandler) TogglePlugin(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if h.Plugins == nil || h.Plugins.IsProtected(id) {
		http.Error(w, "cannot toggle this plugin", http.StatusBadRequest)
		return
	}
	enabled := !h.Plugins.IsEnabled(id)
	h.Plugins.SetEnabled(id, enabled)
	http.Redirect(w, r, "/admin/plugins", http.StatusSeeOther)
}
