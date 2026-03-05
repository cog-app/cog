package web

import (
	"errors"
	"fmt"
	"iter"
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/ui"
)

// groupHandler handles user-facing group management.
type groupHandler struct{ p *services.Providers }

// canManageGroup returns true if the user is the group owner or an admin.
func (h *groupHandler) canManageGroup(groupID string, u *user.User) bool {
	if u.IsAdmin {
		return true
	}
	isOwner, err := h.p.Groups.IsOwnerMember(groupID, u.ID)
	return err == nil && isOwner
}

// ListGroups shows owned + member groups at GET /settings/groups.
func (h *groupHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pd := pageDataWithNav(r, "Groups", h.p.CSRFSecret, h.p.Plugins)

	// Collect owned groups to build a set for filtering.
	ownedGroups, _ := seq.Collect(h.p.Groups.ListOwnedByUser(u.ID))
	ownedSet := make(map[string]bool, len(ownedGroups))
	for _, g := range ownedGroups {
		ownedSet[g.ID] = true
	}

	// Filter member groups, check emptiness with Peek.
	_, ownedIter, hasOwnedErr := seq.Peek(seq.Filter(h.p.Groups.ListOwnedByUser(u.ID), func(g group.Group) bool { return true }))
	hasOwned := !errors.Is(hasOwnedErr, seq.ErrEmpty)
	if !hasOwned {
		ownedIter = seq.Filter(h.p.Groups.ListOwnedByUser(u.ID), func(g group.Group) bool { return true })
	}

	filteredMember := seq.Filter(h.p.Groups.ListGroupsForUser(u.ID), func(g group.Group) bool {
		return !ownedSet[g.ID]
	})
	_, memberIter, hasMemberErr := seq.Peek(filteredMember)
	hasMember := !errors.Is(hasMemberErr, seq.ErrEmpty)
	if !hasMember {
		memberIter = seq.Filter(h.p.Groups.ListGroupsForUser(u.ID), func(g group.Group) bool {
			return !ownedSet[g.ID]
		})
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	settingsGroupsPage(pd, ownedIter, hasOwned, memberIter, hasMember, flashMsg, flashKind).Render(r.Context(), w)
}

// ShowGroup shows group detail at GET /settings/groups/{id}.
func (h *groupHandler) ShowGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")
	g, err := h.p.Groups.Get(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	isOwner := h.canManageGroup(id, u)

	// Check if user is a member or owner — iterate directly, short-circuit.
	if !isOwner {
		isMember := false
		for m, err := range h.p.Groups.ListMembers(id) {
			if err != nil {
				break
			}
			if m.MemberID == u.ID {
				isMember = true
				break
			}
		}
		if !isMember {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	h.renderGroupDetail(w, r, g, isOwner)
}

func (h *groupHandler) renderGroupDetail(w http.ResponseWriter, r *http.Request, g *group.Group, isOwner bool) {
	pd := pageDataWithNav(r, "Group: "+g.Name, h.p.CSRFSecret, h.p.Plugins)
	userNames, _ := seq.ToMap(h.p.Users.List(), func(u user.User) string { return u.ID }, func(u user.User) string { return u.Username })

	var invitations iter.Seq2[group.Invitation, error]
	var hasInvitations bool
	var groupMounts iter.Seq2[mount.GroupMount, error]
	var hasGroupMounts bool
	var groupVolumes iter.Seq2[volume.Volume, error]
	var hasGroupVolumes bool
	var accessibleVolumes []volume.Volume

	if isOwner {
		var invErr error
		_, invitations, invErr = seq.Peek(h.p.Invitations.ListByGroup(g.ID))
		hasInvitations = !errors.Is(invErr, seq.ErrEmpty)
		if !hasInvitations {
			invitations = h.p.Invitations.ListByGroup(g.ID)
		}

		var gmErr error
		_, groupMounts, gmErr = seq.Peek(h.p.Mounts.ListGroupMounts(g.ID))
		hasGroupMounts = !errors.Is(gmErr, seq.ErrEmpty)
		if !hasGroupMounts {
			groupMounts = h.p.Mounts.ListGroupMounts(g.ID)
		}

		// Find group-owned volumes.
		groupVolFilter := seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
			return v.OwnerType == "group" && v.OwnerID != nil && *v.OwnerID == g.ID
		})
		var gvErr error
		_, groupVolumes, gvErr = seq.Peek(groupVolFilter)
		hasGroupVolumes = !errors.Is(gvErr, seq.ErrEmpty)
		if !hasGroupVolumes {
			groupVolumes = seq.Filter(h.p.Volumes.List(), func(v volume.Volume) bool {
				return v.OwnerType == "group" && v.OwnerID != nil && *v.OwnerID == g.ID
			})
		}

		// Accessible volumes for mount creation (need slice for <select>).
		u := auth.UserFromContext(r.Context())
		if h.p.Access != nil {
			accessibleVolumes, _ = seq.Collect(h.p.Access.ListAccessible(u.ID))
		}
	}

	volumeNames := make(map[string]string, len(accessibleVolumes))
	for _, v := range accessibleVolumes {
		volumeNames[v.ID] = v.Name
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind, flashURL string
	if flash != nil {
		flashMsg, flashKind, flashURL = flash.Message, flash.Kind, flash.URL
	}

	smtpAvailable := h.p.Email != nil
	settingsGroupDetailPage(pd, g, h.p.Groups.ListMembers(g.ID), userNames, invitations, hasInvitations, groupMounts, hasGroupMounts, groupVolumes, hasGroupVolumes, accessibleVolumes, volumeNames, isOwner, smtpAvailable, h.p.Config.BaseURL, flashMsg, flashKind, flashURL).Render(r.Context(), w)
}

// CreateGroup handles POST /settings/groups.
func (h *groupHandler) CreateGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Group name is required.")
		http.Redirect(w, r, "/settings/groups", http.StatusSeeOther)
		return
	}

	g, err := h.p.Groups.Create(name, nil, nil)
	if err != nil {
		setFlash(w, "error", "Failed to create group: "+err.Error())
		http.Redirect(w, r, "/settings/groups", http.StatusSeeOther)
		return
	}

	// Add creator as an owner member.
	h.p.Groups.AddMember(g.ID, u.ID, "OWNER")

	http.Redirect(w, r, "/settings/groups/"+g.ID, http.StatusSeeOther)
}

// UpdateGroup handles POST /settings/groups/{id}.
func (h *groupHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")
	if _, err := h.p.Groups.Get(id); err != nil {
		http.NotFound(w, r)
		return
	}
	if !h.canManageGroup(id, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	name := r.FormValue("name")
	if err := h.p.Groups.Update(id, name); err != nil {
		setFlash(w, "error", "Failed to update group.")
	} else {
		setFlash(w, "success", "Group updated.")
	}
	http.Redirect(w, r, "/settings/groups/"+id, http.StatusSeeOther)
}

// AddMember handles POST /settings/groups/{id}/members.
func (h *groupHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	username := r.FormValue("username")
	target, err := h.p.Users.GetByUsername(username)
	if err != nil {
		setFlash(w, "error", "User not found.")
		http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
		return
	}

	h.p.Groups.AddMember(groupID, target.ID, "USER")
	http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
}

// RemoveMember handles DELETE /settings/groups/{id}/members/{member_id}.
func (h *groupHandler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	memberID := r.PathValue("member_id")

	// Allow self-removal (leave) or owner/admin removal.
	if memberID != u.ID && !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	h.p.Groups.RemoveMember(groupID, memberID)
	w.WriteHeader(http.StatusOK)
}

// UpdateMemberType handles POST /settings/groups/{id}/members/{member_id}/type.
func (h *groupHandler) UpdateMemberType(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	memberID := r.PathValue("member_id")

	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	memberType := r.FormValue("member_type")
	if memberType != "USER" && memberType != "OWNER" {
		http.Error(w, "invalid member type", http.StatusBadRequest)
		return
	}

	if err := h.p.Groups.UpdateMemberType(groupID, memberID, memberType); err != nil {
		setFlash(w, "error", "Failed to update member type.")
	}

	http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
}

// CreateInvite handles POST /settings/groups/{id}/invites.
func (h *groupHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	inviteEmail := r.FormValue("email")
	memberType := r.FormValue("member_type")
	if memberType != "OWNER" {
		memberType = "USER"
	}
	expiryHours := settings.DefaultInviteExpiryHours
	if h.p.Settings != nil {
		expiryHours = h.p.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
	}
	inv, err := h.p.Invitations.Create(groupID, inviteEmail, u.ID, expiryHours, memberType)
	if err != nil {
		setFlash(w, "error", "Failed to create invite: "+err.Error())
		http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
		return
	}

	inviteURL := fmt.Sprintf("%s/invite/%s", h.p.Config.BaseURL, inv.Token)

	// Send email if configured and email provided.
	if h.p.Email != nil && inviteEmail != "" {
		g, _ := h.p.Groups.Get(groupID)
		inviterName := u.DisplayName
		if inviterName == "" {
			inviterName = u.Username
		}
		if err := h.p.Email.SendGroupInvite(inviteEmail, g.Name, inviterName, inviteURL); err != nil {
			setFlash(w, "error", "Invite created but email failed: "+err.Error())
			http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
			return
		}
	}

	setFlashURL(w, "success", "Invite created.", inviteURL)
	http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
}

// RevokeInvite handles DELETE /settings/groups/{id}/invites/{invite_id}.
func (h *groupHandler) RevokeInvite(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	inviteID := r.PathValue("invite_id")
	h.p.Invitations.Delete(inviteID)
	w.WriteHeader(http.StatusOK)
}

// AcceptInviteForm handles GET /invite/{token}.
// Works for both authenticated and unauthenticated users.
func (h *groupHandler) AcceptInviteForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.p.Invitations.GetByToken(token)
	if err != nil {
		http.Error(w, "Invalid or expired invitation.", http.StatusNotFound)
		return
	}

	g, err := h.p.Groups.Get(inv.GroupID)
	if err != nil {
		http.Error(w, "Group not found.", http.StatusNotFound)
		return
	}

	u := auth.UserFromContext(r.Context())
	isLoggedIn := u != nil

	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}

	if isLoggedIn {
		pd := pageDataWithNav(r, "Group Invitation", h.p.CSRFSecret, h.p.Plugins)
		inviteAcceptPage(pd, g, inv, true, errMsg).Render(r.Context(), w)
	} else {
		pd := ui.PageData{Title: "Group Invitation"}
		inviteAcceptPage(pd, g, inv, false, errMsg).Render(r.Context(), w)
	}
}

// AcceptInvite handles POST /invite/{token}.
// Works for both authenticated and unauthenticated users.
func (h *groupHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.p.Invitations.GetByToken(token)
	if err != nil {
		http.Error(w, "Invalid or expired invitation.", http.StatusNotFound)
		return
	}

	u := auth.UserFromContext(r.Context())

	if u != nil {
		// Authenticated user — verify CSRF and accept.
		sessID := auth.SessionIDFromContext(r.Context())
		if sessID != "" && !auth.ValidateCSRF(r, sessID, h.p.CSRFSecret) {
			http.Error(w, "Invalid CSRF token", http.StatusForbidden)
			return
		}
		h.p.Groups.AddMember(inv.GroupID, u.ID, inv.MemberType)
		h.p.Invitations.Delete(inv.ID)
		http.Redirect(w, r, "/settings/groups/"+inv.GroupID, http.StatusSeeOther)
		return
	}

	// Unauthenticated user — register and accept.
	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}
	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

	newUser, err := h.p.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Registration failed: "+err.Error())
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

	// Create root mount.
	if h.p.Volumes != nil && h.p.Mounts != nil {
		if defVol, err := h.p.Volumes.GetByName("default"); err == nil {
			subpath := newUser.Username + "/files"
			h.p.Mounts.Create(newUser.ID, "/", defVol.ID, subpath, false)
		}
	}

	// Add to group and delete invite.
	h.p.Groups.AddMember(inv.GroupID, newUser.ID, inv.MemberType)
	h.p.Invitations.Delete(inv.ID)

	// Create session and log in.
	sess, err := h.p.Sessions.Create(newUser.ID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/settings/groups/"+inv.GroupID, http.StatusSeeOther)
}

// CreateGroupMount handles POST /settings/groups/{id}/mounts.
func (h *groupHandler) CreateGroupMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	volumeID := r.FormValue("volume_id")
	userPath := r.FormValue("user_path")
	volumeSubpath := r.FormValue("volume_subpath")

	if volumeID == "" || userPath == "" {
		setFlash(w, "error", "Volume and path are required.")
		http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
		return
	}

	_, err := h.p.Mounts.CreateGroupMount(groupID, userPath, volumeID, volumeSubpath, false)
	if err != nil {
		setFlash(w, "error", "Failed to create mount: "+err.Error())
	} else {
		setFlash(w, "success", "Group mount added.")
	}
	http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
}

// DeleteGroupMount handles DELETE /settings/groups/{id}/mounts/{mount_id}.
func (h *groupHandler) DeleteGroupMount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	mountID := r.PathValue("mount_id")
	h.p.Mounts.DeleteGroupMount(mountID)
	w.WriteHeader(http.StatusOK)
}

// CreateGroupVolume handles POST /settings/groups/{id}/volumes.
func (h *groupHandler) CreateGroupVolume(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Volume name is required.")
		http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
		return
	}

	uri := fmt.Sprintf("file://data/groups/%s/%s", groupID, name)
	g, _ := h.p.Groups.Get(groupID)
	_, err := h.p.Volumes.CreateWithOwnerType(name, "Group volume for "+g.Name, uri, u.Username, &groupID, "group", "{}")
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
	} else {
		setFlash(w, "success", "Group volume created.")
	}
	http.Redirect(w, r, "/settings/groups/"+groupID, http.StatusSeeOther)
}
