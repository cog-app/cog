package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/email"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

// groupDetailData holds all data needed by the shared group detail template.
type groupDetailData struct {
	Group         *group.Group
	Members       []group.Member
	MembersErr    error // listing stopped early; Members holds what came before
	OwnerCount    int   // how many members are owners: an owner may leave only if another remains
	Invitations   []group.Invitation
	IsOwner       bool
	IsAdmin       bool
	SMTPAvailable bool
	SiteBaseURL   string
	BasePath      string // "/settings/groups" or "/admin/groups"
	Flash         string
	FlashKind     string
	LastInviteURL string
}

// groupHandler handles user-facing group management.
type groupHandler struct{ handler }

// groupBasePath returns the base path for group URLs based on the request path.
// Returns "/admin/groups" for admin routes, "/settings/groups" otherwise.
func groupBasePath(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		return "/admin/groups"
	}
	return "/settings/groups"
}

// canManageGroup returns true if the user is the group owner or an admin.
func (h *groupHandler) canManageGroup(groupID string, u *user.User) bool {
	if u.IsAdmin {
		return true
	}
	isOwner, err := h.p.Groups.IsOwnerMember(groupID, u.ID)
	if err != nil {
		h.p.Log.Error("check group ownership", "group", groupID, "user", u.ID, "err", err)
		return false
	}
	return isOwner
}

// ListGroups shows owned + member groups at GET /settings/groups.
func (h *groupHandler) ListGroups(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pd := pageDataWithNav(r, "Groups", h.p.CSRFSecret, h.p.Plugins)

	ownedGroups, err := seq.Collect(h.p.Groups.ListOwnedByUser(u.ID))
	if err != nil {
		h.p.Log.Error("list owned groups", "user", u.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ownedSet := make(map[string]bool, len(ownedGroups))
	for _, g := range ownedGroups {
		ownedSet[g.ID] = true
	}

	allMember, err := seq.Collect(h.p.Groups.ListGroupsForUser(u.ID))
	if err != nil {
		h.p.Log.Error("list member groups", "user", u.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	var memberGroups []group.Group
	for _, g := range allMember {
		if !ownedSet[g.ID] {
			memberGroups = append(memberGroups, g)
		}
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.Render(w, r, settingsGroupsPage(pd, ownedGroups, memberGroups, flashMsg, flashKind))
}

// ShowGroup shows group detail at GET /settings/groups/{id}.
func (h *groupHandler) ShowGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")
	g, err := h.p.Groups.Get(id)
	if errors.Is(err, group.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("show group: look up group", "group", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	isOwner := h.canManageGroup(id, u)

	// Check if user is a member or owner — iterate directly, short-circuit.
	if !isOwner {
		isMember := false
		for m, err := range h.p.Groups.ListMembers(id) {
			if err != nil {
				h.p.Log.Error("show group: list members", "group", id, "err", err)
				http.Error(w, "Internal error", http.StatusInternalServerError)
				return
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

// buildGroupDetailData gathers all data needed by the shared group detail template.
func (h *groupHandler) buildGroupDetailData(w http.ResponseWriter, r *http.Request, g *group.Group, isOwner bool, basePath string) (groupDetailData, error) {
	u := auth.UserFromContext(r.Context())

	d := groupDetailData{
		Group:         g,
		IsOwner:       isOwner,
		IsAdmin:       u.IsAdmin,
		SMTPAvailable: h.p.EmailSender() != nil,
		SiteBaseURL:   h.p.Config.BaseURL,
		BasePath:      basePath,
	}

	for m, err := range h.p.Groups.ListMembers(g.ID) {
		if err != nil {
			h.p.Log.Error("list group members", "group", g.ID, "err", err)
			d.MembersErr = err
			break
		}
		d.Members = append(d.Members, m)
		if m.MemberType == group.Owner {
			d.OwnerCount++
		}
	}

	if isOwner {
		var err error
		d.Invitations, err = seq.Collect(h.p.Invitations.ListByGroup(g.ID))
		if err != nil {
			return groupDetailData{}, err
		}
	}

	flash := getFlash(w, r)
	if flash != nil {
		d.Flash, d.FlashKind, d.LastInviteURL = flash.Message, flash.Kind, flash.URL
	}

	return d, nil
}

func (h *groupHandler) renderGroupDetail(w http.ResponseWriter, r *http.Request, g *group.Group, isOwner bool) {
	pd := pageDataWithNav(r, "Group: "+g.Name, h.p.CSRFSecret, h.p.Plugins)
	d, err := h.buildGroupDetailData(w, r, g, isOwner, "/settings/groups")
	if err != nil {
		h.p.Log.Error("group detail", "group", g.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, settingsGroupDetailPage(pd, d))
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

	g, err := h.p.Groups.Create(name, nil, &u.ID)
	if err != nil {
		setFlash(w, "error", "Failed to create group: "+err.Error())
		http.Redirect(w, r, "/settings/groups", http.StatusSeeOther)
		return
	}

	// Add creator as an owner member.
	if err := h.p.Groups.AddMember(g.ID, u.ID, group.Owner); err != nil {
		setFlash(w, "error", "Group created but failed to add owner: "+err.Error())
		http.Redirect(w, r, "/settings/groups", http.StatusSeeOther)
		return
	}

	setFlash(w, "success", fmt.Sprintf("Group “%s” created. Invite people below.", g.Name))
	http.Redirect(w, r, "/settings/groups/"+g.ID, http.StatusSeeOther)
}

// UpdateGroup handles POST /settings/groups/{id} and POST /admin/groups/{id}.
func (h *groupHandler) UpdateGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id := r.PathValue("id")
	if _, err := h.p.Groups.Get(id); errors.Is(err, group.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		h.p.Log.Error("update group: look up group", "group", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !h.canManageGroup(id, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	base := groupBasePath(r)
	name := r.FormValue("name")
	if err := h.p.Groups.Update(id, name); err != nil {
		setFlash(w, "error", "Failed to update group.", err)
	} else {
		setFlash(w, "success", "Group updated.")
	}
	http.Redirect(w, r, base+"/"+id, http.StatusSeeOther)
}

// AddMember handles POST /settings/groups/{id}/members and POST /admin/groups/{id}/members.
func (h *groupHandler) AddMember(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	base := groupBasePath(r)
	memberID := r.FormValue("member_id")
	if memberID == "" {
		setFlash(w, "error", "Please select a user.")
		http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
		return
	}

	memberType := group.MemberType(r.FormValue("member_type"))
	if memberType != group.GroupMember {
		memberType = group.User
	}

	if err := h.p.Groups.AddMember(groupID, memberID, memberType); err != nil {
		setFlash(w, "error", "Failed to add member: "+err.Error())
	}
	http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
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

	if err := h.p.Groups.RemoveMember(groupID, memberID); err != nil {
		http.Error(w, "Failed to remove member: "+err.Error(), http.StatusInternalServerError)
		return
	}
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

	memberType := group.MemberType(r.FormValue("member_type"))
	if !memberType.IsValid() {
		http.Error(w, "invalid member type", http.StatusBadRequest)
		return
	}

	if err := h.p.Groups.UpdateMemberType(groupID, memberID, memberType); err != nil {
		setFlash(w, "error", "Failed to update member type.", err)
	}

	http.Redirect(w, r, groupBasePath(r)+"/"+groupID, http.StatusSeeOther)
}

// CreateInvite handles POST /settings/groups/{id}/invites and POST /admin/groups/{id}/invites.
func (h *groupHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	base := groupBasePath(r)
	inviteEmail := r.FormValue("email")
	memberType := group.MemberType(r.FormValue("member_type"))
	if memberType != group.Owner {
		memberType = group.User
	}
	expiryHours := settings.DefaultInviteExpiryHours
	if h.p.Settings != nil {
		expiryHours = h.p.Settings.GetInt(settings.KeyInviteExpiryHours, settings.DefaultInviteExpiryHours)
	}
	inv, err := h.p.Invitations.Create(groupID, inviteEmail, u.ID, expiryHours, memberType)
	if err != nil {
		setFlash(w, "error", "Failed to create invite: "+err.Error())
		http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
		return
	}

	inviteURL := fmt.Sprintf("%s/invite/%s", h.p.Config.BaseURL, inv.Token)

	// Send email if configured and email provided.
	if sender := h.p.EmailSender(); sender != nil && inviteEmail != "" {
		g, err := h.p.Groups.Get(groupID)
		if err != nil {
			setFlashURL(w, "error", "Invite created, but the email could not be sent: "+err.Error(), inviteURL)
			http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
			return
		}
		if err := sender.SendGroupInvite(h.p.Settings, email.EmailData{
			BaseURL:      h.p.Config.BaseURL,
			InstanceName: "Cog",
			User:         email.UserInfo{Email: inviteEmail},
			GroupName:    g.Name,
			Inviter:      userInfo(u, h.p.Config.BaseURL),
			InviteURL:    inviteURL,
		}); err != nil {
			setFlash(w, "error", "Invite created but email failed: "+err.Error())
			http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
			return
		}
	}

	setFlashURL(w, "success", "Invite created.", inviteURL)
	http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
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
	if err := h.p.Invitations.DeleteFromGroup(groupID, inviteID); errors.Is(err, group.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "Failed to revoke invite: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// DeleteGroup handles POST /settings/groups/{id}/delete.
func (h *groupHandler) DeleteGroup(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	groupID := r.PathValue("id")
	if !h.canManageGroup(groupID, u) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	base := groupBasePath(r)
	if err := h.p.Groups.Delete(groupID); err != nil {
		setFlash(w, "error", "Failed to delete group: "+err.Error())
		http.Redirect(w, r, base+"/"+groupID, http.StatusSeeOther)
		return
	}
	setFlash(w, "success", "Group deleted.")
	http.Redirect(w, r, base, http.StatusSeeOther)
}

// inviteMayRegister reports whether an invitation from inviter may create an
// account. Any user can make a group and invite to it, so with public
// registration off only an admin's invitation opens a new account; anyone
// else's is for people who already have one.
func (h *groupHandler) inviteMayRegister(inviter *user.User) bool {
	if inviter != nil && inviter.IsAdmin {
		return true
	}
	return h.p.Settings != nil && h.p.Settings.GetBool(settings.KeyPublicRegistration)
}

// AcceptInviteForm handles GET /invite/{token}.
// Works for both authenticated and unauthenticated users.
func (h *groupHandler) AcceptInviteForm(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.p.Invitations.GetByToken(token)
	if err != nil {
		if !errors.Is(err, group.ErrNotFound) {
			h.p.Log.Error("look up invitation", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		http.Error(w, "Invalid or expired invitation.", http.StatusNotFound)
		return
	}

	g, err := h.p.Groups.Get(inv.GroupID)
	if err != nil {
		if !errors.Is(err, group.ErrNotFound) {
			h.p.Log.Error("invitation: look up group", "group", inv.GroupID, "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
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

	var inviter *user.User
	if inv.InvitedBy != "" {
		inviter, err = h.p.Users.GetByID(inv.InvitedBy)
		if err != nil {
			h.p.Log.Warn("invitation: look up inviter", "user", inv.InvitedBy, "err", err)
			inviter = nil
		}
	}

	pd := ui.PageData{Title: "Group Invitation"}
	if isLoggedIn {
		pd = pageDataWithNav(r, "Group Invitation", h.p.CSRFSecret, h.p.Plugins)
	}
	if err := inviteAcceptPage(pd, g, inv, inviter, isLoggedIn, h.inviteMayRegister(inviter), errMsg).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render invitation page", "err", err)
	}
}

// AcceptInvite handles POST /invite/{token}.
// Works for both authenticated and unauthenticated users.
func (h *groupHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	inv, err := h.p.Invitations.GetByToken(token)
	if err != nil {
		if !errors.Is(err, group.ErrNotFound) {
			h.p.Log.Error("look up invitation", "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
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
		if err := h.p.Groups.AddMember(inv.GroupID, u.ID, inv.MemberType); err != nil {
			setFlash(w, "error", "Failed to join group: "+err.Error())
			http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
			return
		}
		if err := h.p.Invitations.Delete(inv.ID); err != nil {
			// Group joined but invite cleanup failed — non-fatal.
			h.p.Log.Error("delete accepted invitation", "invitation", inv.ID, "err", err)
		}
		http.Redirect(w, r, "/settings/groups/"+inv.GroupID, http.StatusSeeOther)
		return
	}

	// Unauthenticated user — register and accept.
	// Verify the invite token was submitted as a form field (CSRF protection).
	if r.FormValue("invite_token") != token {
		http.Error(w, "Invalid request", http.StatusForbidden)
		return
	}
	inviter, err := h.p.Users.GetByID(inv.InvitedBy)
	if err != nil && !errors.Is(err, user.ErrNotFound) {
		h.p.Log.Error("invitation: look up inviter", "user", inv.InvitedBy, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !h.inviteMayRegister(inviter) {
		setFlash(w, "error", "Log in to accept this invitation. New accounts can only be made from an administrator's invitation.")
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

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

	if err := createRootMount(h.p, newUser); err != nil {
		setFlash(w, "error", "Account created but mount setup failed: "+err.Error())
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}

	// Add to group and delete invite.
	if err := h.p.Groups.AddMember(inv.GroupID, newUser.ID, inv.MemberType); err != nil {
		setFlash(w, "error", "Account created but failed to join group: "+err.Error())
		http.Redirect(w, r, "/invite/"+token, http.StatusSeeOther)
		return
	}
	if err := h.p.Invitations.Delete(inv.ID); err != nil {
		// Non-fatal: group joined but invite cleanup failed.
		h.p.Log.Error("delete accepted invitation", "invitation", inv.ID, "err", err)
	}

	// Create session and log in.
	sess, err := h.p.Sessions.Start(newUser.ID, r)
	if err != nil {
		h.p.Log.Error("create session after invite registration", "user", newUser.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	sess.SetCookie(w, cookieCfg)
	http.Redirect(w, r, "/settings/groups/"+inv.GroupID, http.StatusSeeOther)
}
