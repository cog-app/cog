package compat

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// SharingHandler implements the OCS files_sharing API.
type SharingHandler struct {
	Log           *slog.Logger
	Files         *files.Plugin
	Shares        *share.Store
	Users         *user.Store
	Groups        *group.Store
	OnShareCreate func(rec *share.Record)
	OnShareDelete func(rec *share.Record)
}

// ocsShare is the NC-compatible share response format.
type ocsShare struct {
	ID                   int64   `json:"id"`
	ShareType            int     `json:"share_type"`
	UIDOwner             string  `json:"uid_owner"`
	DisplayNameOwner     string  `json:"displayname_owner"`
	Permissions          int     `json:"permissions"`
	CanEdit              bool    `json:"can_edit"`
	CanDelete            bool    `json:"can_delete"`
	STime                int64   `json:"stime"`
	Parent               *string `json:"parent"`
	UIDFileOwner         string  `json:"uid_file_owner"`
	Note                 string  `json:"note"`
	Label                string  `json:"label"`
	DisplayNameFileOwner string  `json:"displayname_file_owner"`
	Path                 string  `json:"path"`
	ItemType             string  `json:"item_type"`
	MimeType             string  `json:"mimetype"`
	HasPreview           bool    `json:"has_preview"`
	StorageID            string  `json:"storage_id"`
	Storage              int     `json:"storage"`
	ItemSource           int64   `json:"item_source"`
	FileSource           int64   `json:"file_source"`
	FileParent           int64   `json:"file_parent"`
	FileTarget           string  `json:"file_target"`
	ShareWith            string  `json:"share_with,omitempty"`
	ShareWithDisplayname string  `json:"share_with_displayname,omitempty"`
	Token                *string `json:"token"`
	Expiration           *string `json:"expiration"`
	URL                  string  `json:"url,omitempty"`
	MailSend             int     `json:"mail_send"`
	HideDownload         int     `json:"hide_download"`
}

// cogToNCPerms converts Cog permission bits to NC permission bits.
func cogToNCPerms(perms uint32) int {
	nc := 0
	if perms&1 != 0 { // PermRead
		nc |= 1
	}
	if perms&2 != 0 { // PermWrite
		nc |= 2 | 4 | 8 // update + create + delete
	}
	if perms&4 != 0 { // PermReshare
		nc |= 16
	}
	return nc
}

// ncToCogPerms converts NC permission bits to Cog permission bits.
func ncToCogPerms(nc int) uint32 {
	var cog uint32
	if nc&1 != 0 {
		cog |= 1 // PermRead
	}
	if nc&(2|4|8) != 0 {
		cog |= 2 // PermWrite
	}
	if nc&16 != 0 {
		cog |= 4 // PermReshare
	}
	return cog
}

// shareTypeToSubject converts NC share type int to Cog subject type string.
func shareTypeToSubject(shareType int) string {
	switch shareType {
	case 0:
		return "USER"
	case 1:
		return "GROUP"
	case 3:
		return "PUBLIC"
	case 7:
		return "INSTANCE"
	default:
		return "USER"
	}
}

// subjectToShareType converts Cog subject type to NC share type int.
func subjectToShareType(subject string) int {
	switch subject {
	case "USER":
		return 0
	case "GROUP":
		return 1
	case "PUBLIC":
		return 3
	case "INSTANCE":
		return 7
	default:
		return 0
	}
}

func (h *SharingHandler) shareToOCS(rec *share.Record, ownerUser *user.User) (*ocsShare, error) {
	ncID, err := h.Shares.NCIDFor(rec.ID)
	if err != nil {
		return nil, fmt.Errorf("nc id for share %s: %w", rec.ID, err)
	}

	displayOwner := ownerUser.DisplayName
	if displayOwner == "" {
		displayOwner = ownerUser.Username
	}

	ocs := &ocsShare{
		ID:                   ncID,
		ShareType:            subjectToShareType(rec.SubjectType),
		UIDOwner:             ownerUser.Username,
		DisplayNameOwner:     displayOwner,
		Permissions:          cogToNCPerms(rec.Permissions),
		CanEdit:              true,
		CanDelete:            true,
		STime:                rec.CreatedAt,
		UIDFileOwner:         ownerUser.Username,
		DisplayNameFileOwner: displayOwner,
		Storage:              1,
		StorageID:            "home::" + ownerUser.Username,
	}

	if rec.SubjectType == "PUBLIC" && rec.Token != nil {
		ocs.Token = rec.Token
		ocs.URL = "/s/" + *rec.Token
		ocs.ShareWithDisplayname = "(Shared link)"
	}

	if rec.SubjectType == "INSTANCE" {
		ocs.ShareWith = ""
		ocs.ShareWithDisplayname = "Everyone"
	}

	if rec.SubjectType != "PUBLIC" && rec.SubjectType != "INSTANCE" {
		ocs.ShareWith = rec.SubjectID
		// Try to look up display name and username
		if target, err := h.Users.GetByID(rec.SubjectID); err == nil {
			ocs.ShareWith = target.Username
			ocs.ShareWithDisplayname = target.DisplayName
			if ocs.ShareWithDisplayname == "" {
				ocs.ShareWithDisplayname = target.Username
			}
		} else {
			ocs.ShareWithDisplayname = rec.SubjectID
		}
	}

	if rec.Expiration != nil {
		exp := time.Unix(*rec.Expiration, 0).Format("2006-01-02 15:04:05")
		ocs.Expiration = &exp
	}

	// Look up file metadata
	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err == nil {
		meta, err := h.Files.GetByID(ownerUser.Username, fileID)
		if err == nil {
			ocs.Path = meta.Path
			ocs.FileTarget = meta.Path
			ocs.ItemSource = meta.ID
			ocs.FileSource = meta.ID
			ocs.FileParent = 0
			ocs.MimeType = meta.MimeType
			ocs.HasPreview = !meta.IsDir
			if meta.IsDir {
				ocs.ItemType = "folder"
				ocs.MimeType = "httpd/unix-directory"
			} else {
				ocs.ItemType = "file"
			}
		}
	}

	return ocs, nil
}

// resolveShareID converts a path value to an internal share ID.
// It accepts either an NC integer ID or a raw internal share ID string.
func (h *SharingHandler) resolveShareID(pathVal string) (string, error) {
	ncID, err := strconv.ParseInt(pathVal, 10, 64)
	if err == nil {
		return h.Shares.ShareIDFromNC(ncID)
	}
	// Fall back to treating it as an internal share ID.
	return pathVal, nil
}

// HandleListShares handles GET .../shares
func (h *SharingHandler) HandleListShares(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	pathFilter := r.URL.Query().Get("path")
	sharedWithMe := r.URL.Query().Get("shared_with_me") == "true"

	var result []*ocsShare

	if sharedWithMe {
		records, err := seq.Collect(h.Shares.ListBySubject(u.ID))
		if err != nil {
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
			return
		}
		for _, rec := range records {
			owner, err := h.Users.GetByID(rec.OwnerUserID)
			if err != nil {
				continue
			}
			ocs, err := h.shareToOCS(&rec, owner)
			if err != nil {
				continue
			}
			result = append(result, ocs)
		}
	} else {
		records, err := seq.Collect(h.Shares.ListByOwner(u.ID))
		if err != nil {
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
			return
		}
		for _, rec := range records {
			ocs, err := h.shareToOCS(&rec, u)
			if err != nil {
				continue
			}
			// Filter by path if specified
			if pathFilter != "" && ocs.Path != pathFilter {
				continue
			}
			result = append(result, ocs)
		}
	}

	if result == nil {
		result = []*ocsShare{}
	}
	writeOCSReq(w, r, http.StatusOK, result)
}

// HandleCreateShare handles POST .../shares
func (h *SharingHandler) HandleCreateShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	path := r.FormValue("path")
	shareTypeStr := r.FormValue("shareType")
	shareWith := r.FormValue("shareWith")
	permStr := r.FormValue("permissions")
	password := r.FormValue("password")
	expireDateStr := r.FormValue("expireDate")

	if path == "" {
		writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "path required")
		return
	}

	shareType, _ := strconv.Atoi(shareTypeStr)
	permsNC, _ := strconv.Atoi(permStr)
	if permsNC == 0 {
		permsNC = 1 // default: read
	}
	cogPerms := ncToCogPerms(permsNC)

	// Look up the file
	meta, err := h.Files.GetByPath(u.Username, path)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "file not found")
		return
	}

	resourceType := files.ResourceType(meta)
	resourceID := files.IDString(meta)

	var expiration *int64
	if expireDateStr != "" {
		t, err := time.Parse("2006-01-02", expireDateStr)
		if err == nil {
			exp := t.Unix()
			expiration = &exp
		}
	}

	var rec *share.Record
	subjectType := shareTypeToSubject(shareType)

	switch subjectType {
	case "PUBLIC":
		rec, err = h.Shares.CreatePublicLinkWithPassword(
			u.ID, resourceType, resourceID, cogPerms, expiration, password,
		)
	case "INSTANCE":
		rec, err = h.Shares.Create(
			u.ID, resourceType, resourceID, "INSTANCE", "", cogPerms,
		)
	default:
		// Look up the target user by username
		targetUser, lookupErr := h.Users.GetByUsername(shareWith)
		if lookupErr != nil {
			writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share target not found")
			return
		}
		rec, err = h.Shares.Create(
			u.ID, resourceType, resourceID, subjectType, targetUser.ID, cogPerms,
		)
	}

	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}

	if h.OnShareCreate != nil {
		h.OnShareCreate(rec)
	}

	ocs, err := h.shareToOCS(rec, u)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}
	writeOCSReq(w, r, http.StatusOK, ocs)
}

// HandleGetShare handles GET .../shares/{shareId}
func (h *SharingHandler) HandleGetShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID, err := h.resolveShareID(r.PathValue("shareId"))
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	owner, err := h.Users.GetByID(rec.OwnerUserID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "owner not found")
		return
	}

	ocs, err := h.shareToOCS(rec, owner)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}
	writeOCSReq(w, r, http.StatusOK, ocs)
}

// HandleUpdateShare handles PUT .../shares/{shareId}
func (h *SharingHandler) HandleUpdateShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID, err := h.resolveShareID(r.PathValue("shareId"))
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	if rec.OwnerUserID != u.ID {
		writeOCSErrorReq(w, r, http.StatusForbidden, 403, "not your share")
		return
	}

	permStr := r.FormValue("permissions")
	expireDateStr := r.FormValue("expireDate")
	password := r.FormValue("password")

	var permsPtr *uint32
	if permStr != "" {
		nc, _ := strconv.Atoi(permStr)
		cog := ncToCogPerms(nc)
		permsPtr = &cog
	}

	var expPtr **int64
	if expireDateStr != "" {
		if expireDateStr == "null" {
			var nilExp *int64
			expPtr = &nilExp
		} else {
			t, err := time.Parse("2006-01-02", expireDateStr)
			if err == nil {
				exp := t.Unix()
				expVal := &exp
				expPtr = &expVal
			}
		}
	}

	if permsPtr != nil || expPtr != nil {
		if err := h.Shares.Update(shareID, permsPtr, expPtr); err != nil {
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}

	if password != "" {
		if err := h.Shares.SetPassword(shareID, password); err != nil {
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}

	// Re-fetch
	rec, _ = h.Shares.Get(shareID)
	owner, _ := h.Users.GetByID(rec.OwnerUserID)
	ocs, ocsErr := h.shareToOCS(rec, owner)
	if ocsErr != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, ocsErr.Error())
		return
	}
	writeOCSReq(w, r, http.StatusOK, ocs)
}

// HandleDeleteShare handles DELETE .../shares/{shareId}
func (h *SharingHandler) HandleDeleteShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID, err := h.resolveShareID(r.PathValue("shareId"))
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	if rec.OwnerUserID != u.ID {
		writeOCSErrorReq(w, r, http.StatusForbidden, 403, "not your share")
		return
	}

	if h.OnShareDelete != nil {
		h.OnShareDelete(rec)
	}

	if err := h.Shares.Delete(shareID); err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}

	writeOCSReq(w, r, http.StatusOK, []any{})
}

// HandleSharees handles GET .../sharees — user/group search for share autocomplete
func (h *SharingHandler) HandleSharees(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	search := strings.ToLower(r.URL.Query().Get("search"))
	perPage := 200
	if pp := r.URL.Query().Get("perPage"); pp != "" {
		if n, err := strconv.Atoi(pp); err == nil && n > 0 {
			perPage = n
		}
	}

	var exactUsers, users []map[string]any
	total := 0

	for usr, err := range h.Users.List() {
		if err != nil {
			break
		}
		if usr.ID == u.ID {
			continue
		}
		name := usr.DisplayName
		if name == "" {
			name = usr.Username
		}
		if search != "" && !strings.Contains(strings.ToLower(usr.Username), search) && !strings.Contains(strings.ToLower(name), search) {
			continue
		}
		entry := map[string]any{
			"label": name,
			"value": map[string]any{
				"shareType": 0,
				"shareWith": usr.Username,
			},
		}
		if strings.EqualFold(usr.Username, search) || strings.EqualFold(name, search) {
			exactUsers = append(exactUsers, entry)
		} else {
			users = append(users, entry)
		}
		total++
		if total >= perPage {
			break
		}
	}

	var exactGroups, groups []map[string]any

	if h.Groups != nil && total < perPage {
		for grp, err := range h.Groups.List() {
			if err != nil {
				break
			}
			if search != "" && !strings.Contains(strings.ToLower(grp.Name), search) {
				continue
			}
			entry := map[string]any{
				"label": grp.Name,
				"value": map[string]any{
					"shareType": 1,
					"shareWith": grp.ID,
				},
			}
			if strings.EqualFold(grp.Name, search) {
				exactGroups = append(exactGroups, entry)
			} else {
				groups = append(groups, entry)
			}
			total++
			if total >= perPage {
				break
			}
		}
	}

	if exactUsers == nil {
		exactUsers = []map[string]any{}
	}
	if users == nil {
		users = []map[string]any{}
	}
	if exactGroups == nil {
		exactGroups = []map[string]any{}
	}
	if groups == nil {
		groups = []map[string]any{}
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"exact": map[string]any{
			"users":         exactUsers,
			"groups":        exactGroups,
			"remotes":       []any{},
			"remote_groups": []any{},
			"emails":        []any{},
			"circles":       []any{},
			"rooms":         []any{},
		},
		"users":         users,
		"groups":        groups,
		"remotes":       []any{},
		"remote_groups": []any{},
		"emails":        []any{},
		"circles":       []any{},
		"rooms":         []any{},
		"lookup":        []any{},
		"lookupEnabled": false,
	})
}

// HandleShareesRecommended handles GET .../sharees_recommended — recently shared-with targets
func (h *SharingHandler) HandleShareesRecommended(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	var userEntries, groupEntries []map[string]any
	seenUsers := map[string]bool{}
	seenGroups := map[string]bool{}

	// Collect unique share targets from existing shares
	for rec, err := range h.Shares.ListByOwner(u.ID) {
		if err != nil {
			break
		}
		switch rec.SubjectType {
		case "USER":
			if seenUsers[rec.SubjectID] {
				continue
			}
			seenUsers[rec.SubjectID] = true
			target, err := h.Users.GetByID(rec.SubjectID)
			if err != nil {
				continue
			}
			name := target.DisplayName
			if name == "" {
				name = target.Username
			}
			userEntries = append(userEntries, map[string]any{
				"label": name,
				"value": map[string]any{
					"shareType": 0,
					"shareWith": target.Username,
				},
			})
		case "GROUP":
			if h.Groups == nil || seenGroups[rec.SubjectID] {
				continue
			}
			seenGroups[rec.SubjectID] = true
			grp, err := h.Groups.Get(rec.SubjectID)
			if err != nil {
				continue
			}
			groupEntries = append(groupEntries, map[string]any{
				"label": grp.Name,
				"value": map[string]any{
					"shareType": 1,
					"shareWith": grp.ID,
				},
			})
		}
	}

	// Fall back to first few users if no recent shares
	if len(userEntries) == 0 && len(groupEntries) == 0 {
		count := 0
		for usr, err := range h.Users.List() {
			if err != nil {
				break
			}
			if usr.ID == u.ID {
				continue
			}
			name := usr.DisplayName
			if name == "" {
				name = usr.Username
			}
			userEntries = append(userEntries, map[string]any{
				"label": name,
				"value": map[string]any{
					"shareType": 0,
					"shareWith": usr.Username,
				},
			})
			count++
			if count >= 25 {
				break
			}
		}
	}

	if userEntries == nil {
		userEntries = []map[string]any{}
	}
	if groupEntries == nil {
		groupEntries = []map[string]any{}
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"exact": map[string]any{
			"users":         userEntries,
			"groups":        groupEntries,
			"remotes":       []any{},
			"remote_groups": []any{},
			"emails":        []any{},
			"circles":       []any{},
			"rooms":         []any{},
		},
		"users":         []any{},
		"groups":        []any{},
		"remotes":       []any{},
		"remote_groups": []any{},
		"emails":        []any{},
		"circles":       []any{},
		"rooms":         []any{},
		"lookup":        []any{},
		"lookupEnabled": false,
	})
}
