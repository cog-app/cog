package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/ui"
)

type shareHandler struct{ handler }

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
	}

	// Convert to UI types.
	var levelChoices []ui.PermissionLevelChoice
	for _, l := range levels {
		levelChoices = append(levelChoices, ui.PermissionLevelChoice{Label: l.Label, Permissions: l.Permissions})
	}

	// Get existing shares for this resource.
	var shares []ui.ShareDisplayItem
	for rec, err := range h.p.Shares.ListByResource(resType, resID) {
		if err != nil {
			break
		}
		if rec.OwnerUserID != u.ID {
			continue // only show shares owned by this user
		}
		sd := ui.ShareDisplayItem{
			ID:          rec.ID,
			SubjectType: rec.SubjectType,
			PermLabel:   permissionLabel(rec.Permissions, levels),
		}
		switch rec.SubjectType {
		case "USER":
			if su, err := h.p.Users.GetByID(rec.SubjectID); err == nil {
				sd.SubjectName = su.DisplayName
				if sd.SubjectName == "" {
					sd.SubjectName = su.Username
				}
			}
		case "GROUP":
			sd.SubjectName = rec.SubjectID
		case "PUBLIC":
			sd.SubjectName = "Public link"
			if rec.Token != nil {
				sd.Token = *rec.Token
			}
		case "INSTANCE":
			sd.SubjectName = "Everyone"
		}
		shares = append(shares, sd)
	}

	sessID := auth.SessionIDFromContext(r.Context())
	csrf := auth.CSRFToken(sessID, h.p.CSRFSecret)

	panelID := r.URL.Query().Get("scope")
	if panelID == "" {
		panelID = "share-panel"
	}
	ui.SharePanel(panelID, resType, resID, csrf, shares, levelChoices).Render(r.Context(), w)
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

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	rec, err := h.p.Shares.Create(u.ID, resType, resID, subjectType, subjectID, perms)
	if err != nil {
		h.p.Log.Error("share create failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	h.propagateShareCreate(rec)

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

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	rec, err := h.p.Shares.CreatePublicLink(u.ID, resType, resID, perms, nil)
	if err != nil {
		h.p.Log.Error("public link create failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = rec // public links don't propagate to specific users

	scope := r.FormValue("panel_id")
	r.URL.RawQuery = fmt.Sprintf("type=%s&id=%s&scope=%s", resType, resID, scope)
	h.SharePanel(w, r)
}

// DeleteShareHTMX handles DELETE from the share panel (HTMX).
func (h *shareHandler) DeleteShareHTMX(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	shareID := r.PathValue("id")

	rec, err := h.p.Shares.Get(shareID)
	if err != nil {
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if rec.OwnerUserID != u.ID {
		http.Error(w, "not your share", http.StatusForbidden)
		return
	}

	h.propagateShareDelete(rec)

	if err := h.p.Shares.Delete(shareID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
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
			break
		}
		if u2.ID == u.ID {
			continue
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
				break
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
				break
			}
			if u2.ID == u.ID {
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

	ui.PickerResults(items).Render(r.Context(), w)
}

func (h *shareHandler) propagateShareCreate(rec *share.Record) {
	if h.p.Search == nil || h.p.Search.Propagator == nil || rec == nil {
		return
	}
	h.p.Search.Propagator.OnShareCreate(&search.ShareRecord{
		ID:           rec.ID,
		OwnerUserID:  rec.OwnerUserID,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		SubjectType:  rec.SubjectType,
		SubjectID:    rec.SubjectID,
	})
}

func (h *shareHandler) propagateShareDelete(rec *share.Record) {
	if h.p.Search == nil || h.p.Search.Propagator == nil || rec == nil {
		return
	}
	h.p.Search.Propagator.OnShareDelete(&search.ShareRecord{
		ID:           rec.ID,
		OwnerUserID:  rec.OwnerUserID,
		ResourceType: rec.ResourceType,
		ResourceID:   rec.ResourceID,
		SubjectType:  rec.SubjectType,
		SubjectID:    rec.SubjectID,
	})
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
