package compat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/activity"
	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/federation"
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
	Federation    *federation.Store
	FedClient     *federation.Client
	Activities    *activity.Store
	BaseURL       string
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
	case 6:
		return "FEDERATED"
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
	case "FEDERATED":
		return 6
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
		ocs.ShareWithDisplayname = rec.SubjectID
		// A user subject has a username and display name to show; a group
		// subject, or a user since deleted, is shown by its ID.
		target, err := h.Users.GetByID(rec.SubjectID)
		switch {
		case errors.Is(err, user.ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("share %s: look up subject: %w", rec.ID, err)
		default:
			ocs.ShareWith = target.Username
			ocs.ShareWithDisplayname = target.DisplayName
			if ocs.ShareWithDisplayname == "" {
				ocs.ShareWithDisplayname = target.Username
			}
		}
	}

	if rec.Expiration != nil {
		exp := time.Unix(*rec.Expiration, 0).Format("2006-01-02 15:04:05")
		ocs.Expiration = &exp
	}

	// Look up file metadata. A resource ID that isn't a number isn't a
	// file, and a file since deleted has no metadata; both are listed
	// without a path.
	if fileID, perr := strconv.ParseInt(rec.ResourceID, 10, 64); perr == nil {
		meta, err := h.Files.GetByID(ownerUser.Username, fileID)
		if err != nil && !errors.Is(err, files.ErrNotFound) {
			return nil, fmt.Errorf("share %s: look up file: %w", rec.ID, err)
		}
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

// lookupShare finds the share the request's {shareId} names. When it can't,
// it writes the OCS error itself — 404 for an unknown share, 500 for a
// lookup failure — and reports false.
func (h *SharingHandler) lookupShare(w http.ResponseWriter, r *http.Request) (*share.Record, bool) {
	shareID, err := h.resolveShareID(r.PathValue("shareId"))
	var rec *share.Record
	if err == nil {
		rec, err = h.Shares.Get(shareID)
	}
	if errors.Is(err, share.ErrNotFound) {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return nil, false
	}
	if err != nil {
		h.Log.Error("look up share", "share", r.PathValue("shareId"), "err", err)
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "look up share failed")
		return nil, false
	}
	return rec, true
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
		// Most shares with one user come from a few owners: look each up once.
		owners := make(map[string]*user.User)
		for _, rec := range records {
			owner, seen := owners[rec.OwnerUserID]
			if !seen {
				owner, err = h.Users.GetByID(rec.OwnerUserID)
				if err != nil && !errors.Is(err, user.ErrNotFound) {
					h.Log.Error("list shares: look up owner", "owner", rec.OwnerUserID, "err", err)
					writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
					return
				}
				owners[rec.OwnerUserID] = owner
			}
			if owner == nil {
				continue // the owner's account is gone
			}
			ocs, err := h.shareToOCS(&rec, owner)
			if err != nil {
				h.Log.Error("list shares", "share", rec.ID, "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
				return
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
				h.Log.Error("list shares", "share", rec.ID, "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
				return
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

	shareType, ok := optionalInt(shareTypeStr)
	if !ok {
		writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid shareType")
		return
	}
	permsNC, ok := optionalInt(permStr)
	if !ok {
		writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid permissions")
		return
	}
	if permsNC == 0 {
		permsNC = 1 // default: read
	}
	cogPerms := ncToCogPerms(permsNC)

	// Look up the file
	meta, err := h.Files.GetByPath(u.Username, path)
	if errors.Is(err, files.ErrNotFound) {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "file not found")
		return
	}
	if err != nil {
		h.Log.Error("create share: look up file", "user", u.Username, "path", path, "err", err)
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "look up file failed")
		return
	}

	resourceType := files.ResourceType(meta)
	resourceID := files.IDString(meta)

	var expiration *int64
	if expireDateStr != "" {
		t, err := time.Parse("2006-01-02", expireDateStr)
		if err != nil {
			writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid expireDate: want YYYY-MM-DD")
			return
		}
		exp := t.Unix()
		expiration = &exp
	}

	var rec *share.Record
	subjectType := shareTypeToSubject(shareType)

	switch subjectType {
	case "FEDERATED":
		h.createFederatedShare(w, r, u, shareWith, path, meta, resourceType, resourceID, cogPerms)
		return
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
		if errors.Is(lookupErr, user.ErrNotFound) {
			writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share target not found")
			return
		}
		if lookupErr != nil {
			h.Log.Error("create share: look up target", "target", shareWith, "err", lookupErr)
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "look up share target failed")
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
	h.logShareActivity(u, rec, "share_created", "shared")

	ocs, err := h.shareToOCS(rec, u)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}
	writeOCSReq(w, r, http.StatusOK, ocs)
}

func (h *SharingHandler) logShareActivity(u *user.User, rec *share.Record, typ, verb string) {
	if h.Activities == nil {
		return
	}
	subject := fmt.Sprintf("%s %s %s", u.DisplayName, verb, rec.ResourceType)
	if err := h.Activities.Log(u.Username, u.Username, "sharing", typ, subject, rec.ResourceType, rec.ResourceID, ""); err != nil {
		slog.Warn("activity log failed", "type", typ, "resource", rec.ResourceID, "err", err)
	}
}

// createFederatedShare handles outbound federated sharing (shareType=6).
func (h *SharingHandler) createFederatedShare(w http.ResponseWriter, r *http.Request, u *user.User, shareWith, filePath string, meta *files.FileMeta, resourceType, resourceID string, cogPerms uint32) {
	// Parse user@host from shareWith.
	at := strings.LastIndex(shareWith, "@")
	if at <= 0 {
		writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "federated share requires user@host format")
		return
	}
	remoteUser := shareWith[:at]
	remoteHost := shareWith[at+1:]

	if h.Federation == nil || h.FedClient == nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "federation not configured")
		return
	}

	// Create a public-link share with a token (reuse existing token mechanism).
	rec, err := h.Shares.CreatePublicLink(u.ID, resourceType, resourceID, cogPerms, nil)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "create share: "+err.Error())
		return
	}

	// Create the outbound federated share record.
	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "pending",
		ShareID:      &rec.ID,
		RemoteHost:   remoteHost,
		RemoteUser:   remoteUser,
		LocalUserID:  u.ID,
		Token:        *rec.Token,
		ResourceName: meta.Name,
		ResourceType: "file",
		OwnerName:    u.DisplayName,
		OwnerHost:    h.BaseURL,
		Permissions:  cogPerms,
	}
	if err := h.Federation.Create(fs); err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "create federation record: "+err.Error())
		return
	}

	// Send the OCM share to the remote server.
	ocmShare := &federation.OCMShare{
		ShareWith:         remoteUser,
		Name:              meta.Name,
		ProviderID:        fs.ID,
		Owner:             u.Username + "@" + h.BaseURL,
		OwnerDisplayName:  u.DisplayName,
		Sender:            u.Username + "@" + h.BaseURL,
		SenderDisplayName: u.DisplayName,
		ShareType:         "user",
		ResourceType:      "file",
		Protocol: federation.OCMProtocol{
			Name: "webdav",
			Options: federation.OCMProtocolOptions{
				SharedSecret: *rec.Token,
				Permissions:  fmt.Sprintf("%d", cogToNCPerms(cogPerms)),
				WebDAVURL:    h.BaseURL + "/public.php/webdav/",
			},
		},
	}

	remoteID, err := h.FedClient.SendShare(context.Background(), remoteHost, ocmShare)
	if err != nil {
		h.Log.Error("send federated share", "remote", remoteHost, "err", err)
		// Clean up: remove the local records.
		if err := h.Shares.Delete(rec.ID); err != nil {
			h.Log.Error("clean up federated share link", "share", rec.ID, "err", err)
		}
		if err := h.Federation.Remove(fs.ID); err != nil {
			h.Log.Error("clean up federation record", "id", fs.ID, "err", err)
		}
		writeOCSErrorReq(w, r, http.StatusBadGateway, 502, "remote server rejected share: "+err.Error())
		return
	}
	if err := h.Federation.SetRemoteID(fs.ID, remoteID); err != nil {
		// The remote accepted the share, so keep it; without its remote ID
		// a later unshare can't be sent to the remote.
		h.Log.Error("record federated share remote id", "id", fs.ID, "remote", remoteHost, "err", err)
	}

	if h.OnShareCreate != nil {
		h.OnShareCreate(rec)
	}

	ocs, err := h.shareToOCS(rec, u)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, err.Error())
		return
	}
	// Override share_type to 6 (FEDERATED) and share_with to the remote user.
	ocs.ShareType = 6
	ocs.ShareWith = shareWith
	ocs.ShareWithDisplayname = remoteUser
	writeOCSReq(w, r, http.StatusOK, ocs)
}

// HandleGetShare handles GET .../shares/{shareId}.
// Only the owner, the share subject, or a member of a subject group may view
// the share. Other users get 404 to avoid existence oracles.
func (h *SharingHandler) HandleGetShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	rec, ok := h.lookupShare(w, r)
	if !ok {
		return
	}

	if !h.shareVisibleTo(rec, u) {
		writeOCSErrorReq(w, r, http.StatusNotFound, 404, "share not found")
		return
	}

	owner, err := h.Users.GetByID(rec.OwnerUserID)
	if err != nil {
		h.Log.Error("get share: look up owner", "owner", rec.OwnerUserID, "err", err)
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

// shareVisibleTo returns true if u may view rec. Owner, user-subject, or
// group-subject member.
func (h *SharingHandler) shareVisibleTo(rec *share.Record, u *user.User) bool {
	if rec.OwnerUserID == u.ID {
		return true
	}
	switch rec.SubjectType {
	case "USER":
		// SubjectID may be user ID or username depending on creator.
		if rec.SubjectID == u.ID || rec.SubjectID == u.Username {
			return true
		}
	case "GROUP":
		if h.Groups == nil {
			return false
		}
		for g, err := range h.Groups.ListGroupsForUser(u.ID) {
			if err != nil {
				// Deny rather than guess; the share reads as not found.
				h.Log.Error("share visibility: list groups", "user", u.ID, "err", err)
				return false
			}
			if g.ID == rec.SubjectID || g.Name == rec.SubjectID {
				return true
			}
		}
	}
	// PUBLIC shares are intentionally not visible via /api/shares/{id} except
	// to the owner — the token is delivered out-of-band by the owner. This
	// prevents enumeration of public-link tokens by any authenticated user.
	return false
}

// HandleUpdateShare handles PUT .../shares/{shareId}
func (h *SharingHandler) HandleUpdateShare(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		writeOCSErrorReq(w, r, http.StatusUnauthorized, 997, "Unauthorized")
		return
	}

	rec, ok := h.lookupShare(w, r)
	if !ok {
		return
	}
	shareID := rec.ID

	if rec.OwnerUserID != u.ID {
		writeOCSErrorReq(w, r, http.StatusForbidden, 403, "not your share")
		return
	}

	permStr := r.FormValue("permissions")
	expireDateStr := r.FormValue("expireDate")
	password := r.FormValue("password")

	var permsPtr *uint32
	if permStr != "" {
		nc, err := strconv.Atoi(permStr)
		if err != nil {
			writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid permissions")
			return
		}
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
			if err != nil {
				writeOCSErrorReq(w, r, http.StatusBadRequest, 400, "invalid expireDate: want YYYY-MM-DD")
				return
			}
			exp := t.Unix()
			expVal := &exp
			expPtr = &expVal
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

	// Re-fetch — these were silently dropping errors and feeding nils into
	// shareToOCS, which would then NPE.
	rec, err := h.Shares.Get(shareID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "refetch share: "+err.Error())
		return
	}
	owner, err := h.Users.GetByID(rec.OwnerUserID)
	if err != nil {
		writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "refetch owner: "+err.Error())
		return
	}
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

	rec, ok := h.lookupShare(w, r)
	if !ok {
		return
	}
	shareID := rec.ID

	if rec.OwnerUserID != u.ID {
		writeOCSErrorReq(w, r, http.StatusForbidden, 403, "not your share")
		return
	}

	if h.OnShareDelete != nil {
		h.OnShareDelete(rec)
	}
	h.logShareActivity(u, rec, "share_deleted", "unshared")

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

	// Audit security.md #48: non-admins should not be able to enumerate the
	// full user roster. Require at least 2 chars of search before returning
	// any users/groups. Admins keep unrestricted browsing for the picker UI.
	if !u.IsAdmin && len(search) < 2 {
		writeOCSReq(w, r, http.StatusOK, map[string]any{
			"exact": map[string]any{
				"users":   []any{},
				"groups":  []any{},
				"remotes": []any{},
			},
			"users":   []any{},
			"groups":  []any{},
			"remotes": []any{},
		})
		return
	}

	var exactUsers, users []map[string]any
	total := 0

	for usr, err := range h.Users.List() {
		if err != nil {
			h.Log.Error("sharees: list users", "err", err)
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "list users failed")
			return
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
				h.Log.Error("sharees: list groups", "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "list groups failed")
				return
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

	// If search contains @, offer it as a federated share candidate.
	var remotes []map[string]any
	var exactRemotes []map[string]any
	if strings.Contains(search, "@") {
		entry := map[string]any{
			"label": search,
			"value": map[string]any{
				"shareType": 6,
				"shareWith": search,
			},
		}
		exactRemotes = append(exactRemotes, entry)
	}
	if remotes == nil {
		remotes = []map[string]any{}
	}
	if exactRemotes == nil {
		exactRemotes = []map[string]any{}
	}

	writeOCSReq(w, r, http.StatusOK, map[string]any{
		"exact": map[string]any{
			"users":         exactUsers,
			"groups":        exactGroups,
			"remotes":       exactRemotes,
			"remote_groups": []any{},
			"emails":        []any{},
			"circles":       []any{},
			"rooms":         []any{},
		},
		"users":         users,
		"groups":        groups,
		"remotes":       remotes,
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
			h.Log.Error("recommended sharees: list shares", "user", u.ID, "err", err)
			writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "list shares failed")
			return
		}
		switch rec.SubjectType {
		case "USER":
			if seenUsers[rec.SubjectID] {
				continue
			}
			seenUsers[rec.SubjectID] = true
			target, err := h.Users.GetByID(rec.SubjectID)
			if errors.Is(err, user.ErrNotFound) {
				continue
			}
			if err != nil {
				h.Log.Error("recommended sharees: look up user", "user", rec.SubjectID, "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "look up user failed")
				return
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
			if errors.Is(err, group.ErrNotFound) {
				continue
			}
			if err != nil {
				h.Log.Error("recommended sharees: look up group", "group", rec.SubjectID, "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "look up group failed")
				return
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
				h.Log.Error("recommended sharees: list users", "err", err)
				writeOCSErrorReq(w, r, http.StatusInternalServerError, 500, "list users failed")
				return
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

// optionalInt parses an optional integer form value: empty is 0, anything
// else must be a number.
func optionalInt(v string) (int, bool) {
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil
}
