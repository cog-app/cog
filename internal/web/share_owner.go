package web

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// ownsResource reports whether u owns the resource a share would name. A
// share is a record of its owner, the resource's type and its ID; nothing in
// it proves the owner owns the resource, so whatever creates one must check.
// Volumes and DAV collections are named by global IDs, and a share of
// someone else's would hand over what it names; a plugin resource lives in
// its owner's own database, so it is the user's if it is in theirs.
func (h *shareHandler) ownsResource(u *user.User, resType, resID string) (bool, error) {
	switch resType {
	case volume.ShareResourceType:
		v, err := h.p.Volumes.GetByID(resID)
		if errors.Is(err, volume.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return v.OwnerUserID != nil && *v.OwnerUserID == u.ID, nil
	case dav.ShareType:
		id, err := strconv.ParseInt(resID, 10, 64)
		if err != nil {
			return false, nil
		}
		c, err := h.p.DAV.Collections.ByID(dav.CollectionID(id))
		if errors.Is(err, dav.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return c.OwnerID == u.ID, nil
	}

	def, ok := h.p.Plugins.ResourceDefByType(resType)
	if !ok || def.Table == "" || def.IDColumn == "" {
		return false, nil
	}
	pluginID, _, _ := strings.Cut(resType, ":")
	db, err := h.p.Plugins.PluginDB(u.Username, pluginID)
	if err != nil {
		return false, fmt.Errorf("open %s database: %w", pluginID, err)
	}
	var one int
	err = db.QueryRow(`SELECT 1 FROM "`+def.Table+`" WHERE "`+def.IDColumn+`" = ?`, resID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up %s %s: %w", resType, resID, err)
	}
	// What is in a folder shared with them is in their files too, but is
	// theirs to share only if the share says so.
	if pluginID == files.PluginID && h.p.Shared != nil {
		id, err := strconv.ParseInt(resID, 10, 64)
		if err != nil {
			return false, nil
		}
		meta, err := h.p.Shared.Files.GetByID(u.Username, id)
		if err != nil {
			return false, fmt.Errorf("look up %s %s: %w", resType, resID, err)
		}
		if err := h.p.Shared.Files.MayShare(u.Username, meta.Path); errors.Is(err, files.ErrNoReshare) {
			return false, nil
		} else if err != nil {
			return false, err
		}
	}
	return true, nil
}

// requireOwner answers the request and reports false unless u owns the
// resource.
func (h *shareHandler) requireOwner(w http.ResponseWriter, u *user.User, resType, resID string) bool {
	ok, err := h.ownsResource(u, resType, resID)
	if err != nil {
		h.p.Log.Error("share: check resource owner", "type", resType, "id", resID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "You can only share what is yours.", http.StatusForbidden)
		return false
	}
	return true
}
