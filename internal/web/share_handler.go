package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/ui"
)

type shareHandler struct{ handler }

// shareSubjectRefs lists the subjects of recs, for a subjectNamer.
func shareSubjectRefs(recs []share.Record) []subjectRef {
	refs := make([]subjectRef, len(recs))
	for i, rec := range recs {
		refs[i] = subjectRef{rec.SubjectType, rec.SubjectID}
	}
	return refs
}

// SharePanel serves the HTMX share panel partial for a resource.
// GET /api/shares/panel?type=calendar:calendar&id=5
func (h *shareHandler) SharePanel(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	resType := r.URL.Query().Get("type")
	resID := r.URL.Query().Get("id")
	if resType == "" || resID == "" {
		http.Error(w, "type and id required", http.StatusBadRequest)
		return
	}

	// Look up permission levels for this resource type.
	levels := defaultPermissionLevels()
	if def, ok := h.p.Plugins.ResourceDefByType(resType); ok && len(def.PermissionLevels) > 0 {
		levels = def.PermissionLevels
	} else if resType == dav.ShareType {
		levels = davPermissionLevels
	}

	// Convert to UI types.
	var levelChoices []ui.PermissionLevelChoice
	for _, l := range levels {
		levelChoices = append(levelChoices, ui.PermissionLevelChoice{Label: l.Label, Permissions: l.Permissions})
	}

	// Get existing shares for this resource.
	// A collection's public link is its feed, whose name says what it holds.
	feedHome := dav.HomeCalendars
	if resType == dav.ShareType {
		id, err := strconv.ParseInt(resID, 10, 64)
		if err != nil {
			http.Error(w, "invalid collection", http.StatusBadRequest)
			return
		}
		c, err := h.p.DAV.Collections.ByID(dav.CollectionID(id))
		switch {
		case errors.Is(err, dav.ErrNotFound):
			http.NotFound(w, r)
			return
		case err != nil:
			h.p.Log.Error("share panel: look up collection", "id", resID, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		feedHome = c.Home
	}
	var recs []share.Record
	for rec, err := range h.p.Shares.ListByResource(resType, resID) {
		if err != nil {
			h.p.Log.Error("share panel: list shares", "type", resType, "id", resID, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if rec.OwnerUserID != u.ID {
			continue // only show shares owned by this user
		}
		recs = append(recs, rec)
	}
	namer, err := newSubjectNamer(h.p, shareSubjectRefs(recs))
	if err != nil {
		h.p.Log.Error("share panel: look up sharees", "type", resType, "id", resID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	var shares []ui.ShareDisplayItem
	for _, rec := range recs {
		sd := ui.ShareDisplayItem{
			ID:          rec.ID,
			SubjectType: rec.SubjectType,
			PermLabel:   permissionLabel(rec.Permissions, levels),
			SubjectName: namer.name(subjectRef{rec.SubjectType, rec.SubjectID}, "(deleted user)"),
		}
		switch rec.SubjectType {
		case "PUBLIC":
			if rec.Token != nil {
				sd.Token = *rec.Token
				sd.LinkPath = "/s/" + sd.Token
				if resType == dav.ShareType {
					// A calendar's public link is its ICS feed, an
					// address book's a vCard file of its cards.
					sd.SubjectName = "Feed"
					sd.LinkPath = strings.TrimPrefix(h.p.DAV.FeedURL(sd.Token, feedHome), strings.TrimSuffix(h.p.Config.BaseURL, "/"))
				}
			}
		}
		shares = append(shares, sd)
	}

	sessID := auth.SessionIDFromContext(r.Context())
	csrf := auth.CSRFToken(sessID, h.p.CSRFSecret)

	panelID := r.URL.Query().Get("scope")
	if panelID == "" {
		panelID = "share-panel"
	}
	if err := ui.SharePanel(panelID, resType, resID, csrf, shares, levelChoices).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render share panel", "err", err)
	}
}

// CreateShareForm handles POST from the share panel form (form-encoded, HTMX).
func (h *shareHandler) CreateShareForm(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	resType := r.FormValue("resource_type")
	resID := r.FormValue("resource_id")
	subjectType := r.FormValue("subject_type")
	subjectID := r.FormValue("subject_id")
	permStr := r.FormValue("permissions")

	if subjectType == "" {
		subjectType = "USER"
	}

	if resType == "" || resID == "" || (subjectType != "INSTANCE" && subjectID == "") {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	if !h.requireOwner(w, u, resType, resID) {
		return
	}

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	if _, err := h.p.Shares.Create(u.ID, resType, resID, subjectType, subjectID, perms); err != nil {
		h.p.Log.Error("share create failed", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	// Return the refreshed panel via HTMX.
	scope := r.FormValue("panel_id")
	r.URL.RawQuery = fmt.Sprintf("type=%s&id=%s&scope=%s", resType, resID, scope)
	h.SharePanel(w, r)
}

// CreatePublicLinkForm handles POST from the share panel public link form (HTMX).
func (h *shareHandler) CreatePublicLinkForm(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	resType := r.FormValue("resource_type")
	resID := r.FormValue("resource_id")
	permStr := r.FormValue("permissions")

	if resType == "" || resID == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}
	if !h.requireOwner(w, u, resType, resID) {
		return
	}

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	// Public links don't propagate to specific users, so the record
	// isn't needed.
	if _, err := h.p.Shares.CreatePublicLink(u.ID, resType, resID, perms, nil); err != nil {
		h.p.Log.Error("public link create failed", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	scope := r.FormValue("panel_id")
	r.URL.RawQuery = fmt.Sprintf("type=%s&id=%s&scope=%s", resType, resID, scope)
	h.SharePanel(w, r)
}

// DeleteShareHTMX handles DELETE from the share panel (HTMX).
func (h *shareHandler) DeleteShareHTMX(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	shareID := r.PathValue("id")

	rec, err := h.p.Shares.Get(shareID)
	if errors.Is(err, share.ErrNotFound) {
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.p.Log.Error("delete share: look up share", "share", shareID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if rec.OwnerUserID != u.ID {
		http.Error(w, "not your share", http.StatusForbidden)
		return
	}

	if err := h.p.Shares.Delete(shareID); err != nil {
		h.p.Log.Error("delete share", "share", shareID, "err", err)
		httperr.Internal(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("")) // empty response for HTMX swap
}

// UserSearch returns users matching a query (for share target autocomplete).
// GET /api/users/search?q=foo
func (h *shareHandler) UserSearch(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	q := strings.ToLower(r.URL.Query().Get("q"))

	var results []map[string]string
	for u2, err := range h.p.Users.List() {
		if err != nil {
			h.p.Log.Error("user search", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		if u2.ID == u.ID || (u2.IsAgent() && u2.OwnerID != u.ID) {
			continue // another user's agent is theirs to share with
		}
		if q != "" && !strings.Contains(strings.ToLower(u2.Username), q) &&
			!strings.Contains(strings.ToLower(u2.DisplayName), q) {
			continue
		}
		name := u2.DisplayName
		if name == "" {
			name = u2.Username
		}
		results = append(results, map[string]string{
			"id":       u2.ID,
			"username": u2.Username,
			"name":     name,
		})
		if len(results) >= 10 {
			break
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// PickerSearch returns HTML partial results for the user/group picker.
// GET /api/picker/search?type=user&q=foo
// GET /api/picker/search?type=group&q=foo
func (h *shareHandler) PickerSearch(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	q := strings.ToLower(r.URL.Query().Get("q"))
	typ := r.URL.Query().Get("type")

	var items []ui.PickerItem

	switch typ {
	case "group":
		for g, err := range h.p.Groups.List() {
			if err != nil {
				h.p.Log.Error("user search", "err", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			if q != "" && !strings.Contains(strings.ToLower(g.Name), q) {
				continue
			}
			items = append(items, ui.PickerItem{
				ID:   g.ID,
				Name: g.Name,
				Sub:  fmt.Sprintf("%d members", g.MemberCount),
			})
			if len(items) >= 10 {
				break
			}
		}
	default: // "user"
		for u2, err := range h.p.Users.List() {
			if err != nil {
				h.p.Log.Error("user search", "err", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			if u2.ID == u.ID || (u2.IsAgent() && u2.OwnerID != u.ID) {
				continue
			}
			name := u2.DisplayName
			if name == "" {
				name = u2.Username
			}
			if q != "" && !strings.Contains(strings.ToLower(u2.Username), q) &&
				!strings.Contains(strings.ToLower(name), q) {
				continue
			}
			items = append(items, ui.PickerItem{
				ID:        u2.ID,
				Name:      name,
				Sub:       u2.Username,
				AvatarURL: u2.AvatarURL(),
			})
			if len(items) >= 10 {
				break
			}
		}
	}

	if err := ui.PickerResults(items).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render picker results", "err", err)
	}
}

// CreateFederatedShareForm handles POST from the share panel federated share form (HTMX).
func (h *shareHandler) CreateFederatedShareForm(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	resType := r.FormValue("resource_type")
	resID := r.FormValue("resource_id")
	shareWith := r.FormValue("share_with")
	permStr := r.FormValue("permissions")

	if resType == "" || resID == "" || shareWith == "" || !strings.Contains(shareWith, "@") {
		http.Error(w, "missing fields or invalid remote user format", http.StatusBadRequest)
		return
	}
	// A federated share is served by the files WebDAV endpoint, so only a
	// file or folder can be one.
	if resType != "files:file" && resType != "files:directory" {
		http.Error(w, "Only files and folders can be shared with another server.", http.StatusBadRequest)
		return
	}
	if !h.requireOwner(w, u, resType, resID) {
		return
	}

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	at := strings.LastIndex(shareWith, "@")
	remoteUser := shareWith[:at]
	remoteHost := shareWith[at+1:]

	if h.p.Federation == nil || h.p.FedClient == nil {
		http.Error(w, "federation not configured", http.StatusInternalServerError)
		return
	}
	allowed, err := h.p.FedPolicy.PeerAllowed(remoteHost)
	if err != nil {
		h.p.Log.Error("federated share: read federation policy", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if !allowed {
		http.Error(w, "Sharing with "+remoteHost+" is not allowed on this server.", http.StatusForbidden)
		return
	}

	// Create a public-link share with token for the resource.
	rec, err := h.p.Shares.CreatePublicLink(u.ID, resType, resID, perms, nil)
	if err != nil {
		h.p.Log.Error("create share for federation", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	resourceName := resID

	// Create outbound federated share record.
	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "pending",
		ShareID:      &rec.ID,
		RemoteHost:   remoteHost,
		RemoteUser:   remoteUser,
		LocalUserID:  u.ID,
		Token:        *rec.Token,
		ResourceName: resourceName,
		ResourceType: "file",
		OwnerName:    u.DisplayName,
		OwnerHost:    h.p.Config.BaseURL,
		Permissions:  perms,
	}
	if err := h.p.Federation.Create(fs); err != nil {
		h.p.Log.Error("create federation record", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	// Send OCM share to remote.
	ocmShare := &federation.OCMShare{
		ShareWith:         remoteUser,
		Name:              resourceName,
		ProviderID:        fs.ID,
		Owner:             u.Username + "@" + h.p.Config.BaseURL,
		OwnerDisplayName:  u.DisplayName,
		Sender:            u.Username + "@" + h.p.Config.BaseURL,
		SenderDisplayName: u.DisplayName,
		ShareType:         "user",
		ResourceType:      "file",
		Protocol: federation.OCMProtocol{
			Name: "webdav",
			Options: federation.OCMProtocolOptions{
				SharedSecret: *rec.Token,
				WebDAVURL:    h.p.Config.BaseURL + "/public.php/webdav/",
			},
		},
	}

	remoteID, err := h.p.FedClient.SendShare(r.Context(), remoteHost, ocmShare)
	if err != nil {
		h.p.Log.Error("send federated share", "remote", remoteHost, "err", err)
		// Clean up.
		if err := h.p.Shares.Delete(rec.ID); err != nil {
			h.p.Log.Error("clean up federated share link", "share", rec.ID, "err", err)
		}
		if err := h.p.Federation.Remove(fs.ID); err != nil {
			h.p.Log.Error("clean up federation record", "id", fs.ID, "err", err)
		}
		http.Error(w, "Remote server rejected: "+err.Error(), http.StatusBadGateway)
		return
	}
	if err := h.p.Federation.SetRemoteID(fs.ID, remoteID); err != nil {
		// The remote accepted the share, so keep it; without its remote ID
		// a later unshare can't be sent to the remote.
		h.p.Log.Error("record federated share remote id", "id", fs.ID, "remote", remoteHost, "err", err)
	}

	// Return refreshed panel.
	scope := r.FormValue("panel_id")
	r.URL.RawQuery = fmt.Sprintf("type=%s&id=%s&scope=%s", resType, resID, scope)
	h.SharePanel(w, r)
}

func defaultPermissionLevels() []plugin.PermissionLevel {
	return []plugin.PermissionLevel{
		{Label: "Read-only", Permissions: plugin.PermRead},
		{Label: "Read/Write", Permissions: plugin.PermRead | plugin.PermWrite},
	}
}

func permissionLabel(perms uint32, levels []plugin.PermissionLevel) string {
	for _, l := range levels {
		if l.Permissions == perms {
			return l.Label
		}
	}
	return strings.Join(share.PermissionNames(perms), ", ")
}
