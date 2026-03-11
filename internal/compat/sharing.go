package compat

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// SharingHandler implements the OCS files_sharing API.
type SharingHandler struct {
	Log    *slog.Logger
	Files  *files.Plugin
	Shares *share.Store
	Users  *user.Store
}

// ocsShare is the NC-compatible share response format.
type ocsShare struct {
	ID                   string `json:"id"`
	ShareType            int    `json:"share_type"`
	UIDOwner             string `json:"uid_owner"`
	DisplayNameOwner     string `json:"displayname_owner"`
	Permissions          int    `json:"permissions"`
	STime                int64  `json:"stime"`
	UIDFileOwner         string `json:"uid_file_owner"`
	DisplayNameFileOwner string `json:"displayname_file_owner"`
	Path                 string `json:"path"`
	ItemType             string `json:"item_type"`
	MimeType             string `json:"mimetype"`
	FileSource           int64  `json:"file_source"`
	FileTarget           string `json:"file_target"`
	Storage              int    `json:"storage"`
	ShareWith            string `json:"share_with,omitempty"`
	ShareWithDisplayname string `json:"share_with_displayname,omitempty"`
	Token                string `json:"token,omitempty"`
	Expiration           string `json:"expiration,omitempty"`
	URL                  string `json:"url,omitempty"`
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
	default:
		return 0
	}
}

func (h *SharingHandler) shareToOCS(rec *share.Record, ownerUser *user.User) *ocsShare {
	ocs := &ocsShare{
		ID:                   rec.ID,
		ShareType:            subjectToShareType(rec.SubjectType),
		UIDOwner:             ownerUser.Username,
		DisplayNameOwner:     ownerUser.DisplayName,
		Permissions:          cogToNCPerms(rec.Permissions),
		STime:                rec.CreatedAt,
		UIDFileOwner:         ownerUser.Username,
		DisplayNameFileOwner: ownerUser.DisplayName,
		Storage:              1,
	}

	if ocs.DisplayNameOwner == "" {
		ocs.DisplayNameOwner = ownerUser.Username
	}
	if ocs.DisplayNameFileOwner == "" {
		ocs.DisplayNameFileOwner = ownerUser.Username
	}

	if rec.SubjectType == "PUBLIC" && rec.Token != nil {
		ocs.Token = *rec.Token
		ocs.URL = "/s/" + *rec.Token
	}

	if rec.SubjectType != "PUBLIC" {
		ocs.ShareWith = rec.SubjectID
		// Try to look up display name
		if target, err := h.Users.GetByID(rec.SubjectID); err == nil {
			ocs.ShareWithDisplayname = target.DisplayName
			if ocs.ShareWithDisplayname == "" {
				ocs.ShareWithDisplayname = target.Username
			}
		} else {
			ocs.ShareWithDisplayname = rec.SubjectID
		}
	}

	if rec.Expiration != nil {
		ocs.Expiration = time.Unix(*rec.Expiration, 0).Format("2006-01-02 15:04:05")
	}

	// Look up file metadata
	fileID, err := strconv.ParseInt(rec.ResourceID, 10, 64)
	if err == nil {
		meta, err := h.Files.GetByID(ownerUser.Username, fileID)
		if err == nil {
			ocs.Path = meta.Path
			ocs.FileTarget = meta.Path
			ocs.FileSource = meta.ID
			ocs.MimeType = meta.MimeType
			if meta.IsDir {
				ocs.ItemType = "folder"
				ocs.MimeType = "httpd/unix-directory"
			} else {
				ocs.ItemType = "file"
			}
		}
	}

	return ocs
}

// HandleListShares handles GET .../shares
func (h *SharingHandler) HandleListShares(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	pathFilter := r.URL.Query().Get("path")
	sharedWithMe := r.URL.Query().Get("shared_with_me") == "true"

	var result []*ocsShare

	if sharedWithMe {
		records, err := seq.Collect(h.Shares.ListBySubject(u.ID))
		if err != nil {
			writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
			return
		}
		for _, rec := range records {
			owner, err := h.Users.GetByID(rec.OwnerUserID)
			if err != nil {
				continue
			}
			result = append(result, h.shareToOCS(&rec, owner))
		}
	} else {
		records, err := seq.Collect(h.Shares.ListByOwner(u.ID))
		if err != nil {
			writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
			return
		}
		for _, rec := range records {
			ocs := h.shareToOCS(&rec, u)
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
	writeOCS(w, http.StatusOK, result)
}

// HandleCreateShare handles POST .../shares
func (h *SharingHandler) HandleCreateShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	path := r.FormValue("path")
	shareTypeStr := r.FormValue("shareType")
	shareWith := r.FormValue("shareWith")
	permStr := r.FormValue("permissions")
	password := r.FormValue("password")
	expireDateStr := r.FormValue("expireDate")

	if path == "" {
		writeOCSError(w, http.StatusBadRequest, 400, "path required")
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
		writeOCSError(w, http.StatusNotFound, 404, "file not found")
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

	if subjectType == "PUBLIC" {
		rec, err = h.Shares.CreatePublicLinkWithPassword(
			u.ID, resourceType, resourceID, cogPerms, expiration, password,
		)
	} else {
		// Look up the target user by username
		targetUser, lookupErr := h.Users.GetByUsername(shareWith)
		if lookupErr != nil {
			writeOCSError(w, http.StatusNotFound, 404, "share target not found")
			return
		}
		rec, err = h.Shares.Create(
			u.ID, resourceType, resourceID, subjectType, targetUser.ID, cogPerms,
		)
	}

	if err != nil {
		writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
		return
	}

	writeOCS(w, http.StatusOK, h.shareToOCS(rec, u))
}

// HandleGetShare handles GET .../shares/{shareId}
func (h *SharingHandler) HandleGetShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID := r.PathValue("shareId")
	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSError(w, http.StatusNotFound, 404, "share not found")
		return
	}

	owner, err := h.Users.GetByID(rec.OwnerUserID)
	if err != nil {
		writeOCSError(w, http.StatusInternalServerError, 500, "owner not found")
		return
	}

	writeOCS(w, http.StatusOK, h.shareToOCS(rec, owner))
}

// HandleUpdateShare handles PUT .../shares/{shareId}
func (h *SharingHandler) HandleUpdateShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID := r.PathValue("shareId")
	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSError(w, http.StatusNotFound, 404, "share not found")
		return
	}

	if rec.OwnerUserID != u.ID {
		writeOCSError(w, http.StatusForbidden, 403, "not your share")
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
			writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}

	if password != "" {
		if err := h.Shares.SetPassword(shareID, password); err != nil {
			writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
			return
		}
	}

	// Re-fetch
	rec, _ = h.Shares.Get(shareID)
	owner, _ := h.Users.GetByID(rec.OwnerUserID)
	writeOCS(w, http.StatusOK, h.shareToOCS(rec, owner))
}

// HandleDeleteShare handles DELETE .../shares/{shareId}
func (h *SharingHandler) HandleDeleteShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	shareID := r.PathValue("shareId")
	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSError(w, http.StatusNotFound, 404, "share not found")
		return
	}

	if rec.OwnerUserID != u.ID {
		writeOCSError(w, http.StatusForbidden, 403, "not your share")
		return
	}

	if err := h.Shares.Delete(shareID); err != nil {
		writeOCSError(w, http.StatusInternalServerError, 500, err.Error())
		return
	}

	writeOCS(w, http.StatusOK, []any{})
}

// HandleSharees handles GET .../sharees — user search for share autocomplete
func (h *SharingHandler) HandleSharees(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSError(w, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	search := strings.ToLower(r.URL.Query().Get("search"))

	var exact, users []map[string]any

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
			exact = append(exact, entry)
		} else {
			users = append(users, entry)
		}
	}

	if exact == nil {
		exact = []map[string]any{}
	}
	if users == nil {
		users = []map[string]any{}
	}

	writeOCS(w, http.StatusOK, map[string]any{
		"exact": map[string]any{
			"users":   exact,
			"groups":  []any{},
			"remotes": []any{},
		},
		"users":   users,
		"groups":  []any{},
		"remotes": []any{},
	})
}
