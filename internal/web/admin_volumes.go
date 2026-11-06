package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/quota"
	"git.duckfam.us/jj-software/cog/internal/seq"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/ui"
)

type adminVolumeHandler struct{ handler }

func (h *adminVolumeHandler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Volumes", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.Render(w, r, adminVolumesPage(pd, h.p.Volumes.List(), flashMsg, flashKind))
}

func (h *adminVolumeHandler) NewVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New Volume", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.Render(w, r, adminVolumeNewPage(pd, errMsg))
}

func (h *adminVolumeHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if name == "" {
		setFlash(w, "error", "Name is required.")
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	uri, config, err := buildVolumeURIAndConfig(r)
	if err != nil {
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	_, err = h.p.Volumes.Create(name, r.FormValue("description"), uri, nil, config)
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/volumes", http.StatusSeeOther)
}

func (h *adminVolumeHandler) EditVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit Volume", h.p.CSRFSecret, h.p.Plugins)
	id := r.PathValue("id")
	v, err := h.p.Volumes.GetByID(id)
	if errors.Is(err, volume.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("edit volume: look up volume", "volume", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Build form data from existing volume.
	fd := volumeFormData{
		Action:           "/admin/volumes/" + v.ID,
		CSRFToken:        pd.CSRFToken,
		Name:             v.Name,
		Description:      v.Description,
		ConnType:         v.Scheme(),
		SubmitLabel:      "Save",
		ShowTypeSelector: true,
		CancelURL:        "/admin/volumes",
	}
	if fd.ConnType == "file" {
		fd.FilePath = v.URIPath()
	} else if fd.ConnType == "webdav" {
		cfg, err := volume.ParseWebDAVConfig(v.Config)
		if err != nil {
			// Show the form with empty WebDAV fields so the admin can
			// re-enter them, rather than refusing to edit the volume.
			h.p.Log.Error("edit volume: parse webdav config", "volume", v.ID, "err", err)
		} else {
			fd.WebDAV = *cfg
		}
	}

	// Load shares for the volume.
	shares, err := buildVolumeShares(h, v.ID)
	if err != nil {
		h.p.Log.Error("edit volume: list shares", "volume", v.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// Load quotas for the volume.
	volQuotas, err := seq.Collect(h.p.Quotas.ListByVolume(v.ID))
	if err != nil {
		h.p.Log.Error("edit volume: list quotas", "volume", v.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	quotaRefs := make([]subjectRef, len(volQuotas))
	for i, q := range volQuotas {
		quotaRefs[i] = subjectRef{q.SubjectType, q.SubjectID}
	}
	quotaNamer, err := newSubjectNamer(h.p, quotaRefs)
	if err != nil {
		h.p.Log.Error("edit volume: quota subjects", "volume", v.ID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	var quotas []quotaDisplayItem
	for i, q := range volQuotas {
		quotas = append(quotas, quotaDisplayItem{
			ID:          q.ID,
			SubjectType: q.SubjectType,
			SubjectName: quotaNamer.name(quotaRefs[i], ""),
			QuotaBytes:  q.QuotaBytes,
			QuotaLabel:  quota.FormatBytes(q.QuotaBytes),
		})
	}

	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	ui.Render(w, r, adminVolumeEditPage(pd, v, fd, v.IsDefault(), shares, defaultPermissionLevels(), quotas, flashMsg, flashKind))
}

func (h *adminVolumeHandler) UpdateVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !h.volumeExists(w, r, id) {
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")

	uri, config, err := buildVolumeURIAndConfig(r)
	if err != nil {
		setFlash(w, "error", err.Error())
		http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
		return
	}

	if err := h.p.Volumes.Update(id, name, description, uri, config); err != nil {
		setFlash(w, "error", "Failed to update: "+err.Error(), err)
	} else {
		setFlash(w, "success", "Volume updated.")
	}
	http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
}

func (h *adminVolumeHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := h.p.Volumes.GetByID(id)
	if errors.Is(err, volume.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		h.p.Log.Error("delete volume: look up volume", "volume", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if v.IsDefault() {
		http.Error(w, "cannot delete the default volume", http.StatusBadRequest)
		return
	}
	if err := h.p.Volumes.Delete(id); err != nil {
		h.p.Log.Error("delete volume", "volume", id, "err", err)
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// SetQuota handles POST /admin/volumes/{id}/quotas.
func (h *adminVolumeHandler) SetQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !h.volumeExists(w, r, id) {
		return
	}

	subjectType := r.FormValue("subject_type")
	subjectID := r.FormValue("subject_id")
	amountStr := r.FormValue("amount")
	unit := r.FormValue("unit")

	amount, err := strconv.ParseInt(amountStr, 10, 64)
	if err != nil || amount <= 0 {
		setFlash(w, "error", "Invalid quota amount.")
		http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
		return
	}

	switch unit {
	case "GB":
		amount *= 1024 * 1024 * 1024
	case "MB":
		amount *= 1024 * 1024
	default:
		setFlash(w, "error", "Invalid unit.")
		http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
		return
	}

	if subjectType == "DEFAULT" {
		subjectID = ""
	}

	if err := h.p.Quotas.Set(id, subjectType, subjectID, amount); err != nil {
		setFlash(w, "error", "Failed to set quota: "+err.Error(), err)
	} else {
		setFlash(w, "success", "Quota set.")
	}
	http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
}

// DeleteQuota handles DELETE /admin/volumes/{id}/quotas/{quota_id}.
func (h *adminVolumeHandler) DeleteQuota(w http.ResponseWriter, r *http.Request) {
	quotaID := r.PathValue("quota_id")
	if err := h.p.Quotas.Delete(quotaID); err != nil {
		h.p.Log.Error("delete quota", "quota", quotaID, "err", err)
		httperr.Internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// volumeExists reports whether the volume exists, answering the request
// itself (404, or 500 on a lookup failure) when it doesn't.
func (h *adminVolumeHandler) volumeExists(w http.ResponseWriter, r *http.Request, id string) bool {
	_, err := h.p.Volumes.GetByID(id)
	if errors.Is(err, volume.ErrNotFound) {
		http.NotFound(w, r)
		return false
	}
	if err != nil {
		h.p.Log.Error("look up volume", "volume", id, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return false
	}
	return true
}

// DirSuggest handles GET /api/admin/dir-suggest?q=/some/path and returns
// directory completions as picker-style HTML results.
func (h *adminVolumeHandler) DirSuggest(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("file_path")
	if q == "" {
		q = "/"
	}

	// Determine parent directory and prefix to filter by.
	dir := q
	prefix := ""
	info, err := os.Stat(q)
	if err != nil || !info.IsDir() {
		// q is a partial path — list parent and filter by basename prefix.
		dir = filepath.Dir(q)
		prefix = filepath.Base(q)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		w.WriteHeader(200)
		ui.Render(w, r, ui.PickerResults(nil))
		return
	}

	var items []ui.PickerItem
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(prefix, ".") {
			continue
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix)) {
			continue
		}
		full := filepath.Join(dir, name)
		items = append(items, ui.PickerItem{
			ID:   full,
			Name: full,
			Sub:  "directory",
		})
		if len(items) >= 20 {
			break
		}
	}
	ui.Render(w, r, ui.PickerResults(items))
}

// buildVolumeURIAndConfig parses form fields for connection type and returns the URI and config JSON.
func buildVolumeURIAndConfig(r *http.Request) (uri, config string, err error) {
	connType := r.FormValue("conn_type")
	switch connType {
	case "file":
		path := r.FormValue("file_path")
		if path == "" {
			return "", "", fmt.Errorf("Path is required for local filesystem volumes.")
		}
		if !filepath.IsAbs(path) {
			return "", "", fmt.Errorf("Path must be absolute (e.g. /mnt/data).")
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return "", "", fmt.Errorf("Path does not exist: %s", path)
		}
		if !info.IsDir() {
			return "", "", fmt.Errorf("Path is not a directory: %s", path)
		}
		return "file://" + path, "{}", nil
	case "webdav":
		remoteURL := r.FormValue("remote_url")
		if remoteURL == "" {
			return "", "", fmt.Errorf("Remote URL is required for WebDAV volumes.")
		}
		name := r.FormValue("name")
		cfg := volume.WebDAVConfig{
			URL:      remoteURL,
			AuthType: r.FormValue("auth_type"),
			Username: r.FormValue("webdav_username"),
			Password: r.FormValue("webdav_password"),
			Token:    r.FormValue("webdav_token"),
		}
		cfgJSON, err := json.Marshal(cfg)
		if err != nil {
			return "", "", fmt.Errorf("encode WebDAV config: %w", err)
		}
		return "webdav://" + name, string(cfgJSON), nil
	case "mem":
		return "mem://", "{}", nil
	default:
		return "", "", fmt.Errorf("Unknown connection type: %s", connType)
	}
}

// quotaDisplayItem holds rendered quota info for templates.
type quotaDisplayItem struct {
	ID          string
	SubjectType string
	SubjectName string
	QuotaBytes  int64
	QuotaLabel  string
}

// buildVolumeShares loads shares for a volume, building UI display items.
// Admin mode: shows ALL shares for the resource, not filtered by owner.
func buildVolumeShares(h *adminVolumeHandler, volumeID string) ([]ui.ShareDisplayItem, error) {
	recs, err := seq.Collect(h.p.Shares.ListByResource(volume.ShareResourceType, volumeID))
	if err != nil {
		return nil, err
	}
	namer, err := newSubjectNamer(h.p, shareSubjectRefs(recs))
	if err != nil {
		return nil, err
	}
	var shares []ui.ShareDisplayItem
	for _, rec := range recs {
		shares = append(shares, ui.ShareDisplayItem{
			ID:          rec.ID,
			SubjectType: rec.SubjectType,
			PermLabel:   permissionLabel(rec.Permissions, defaultPermissionLevels()),
			SubjectName: namer.name(subjectRef{rec.SubjectType, rec.SubjectID}, ""),
		})
	}
	return shares, nil
}

// SharePanelAdmin serves the HTMX share panel for admin volume editing.
// Unlike the regular SharePanel, this shows ALL shares regardless of owner.
func (h *shareHandler) SharePanelAdmin(w http.ResponseWriter, r *http.Request) {
	resType := r.URL.Query().Get("type")
	resID := r.URL.Query().Get("id")
	if resType == "" || resID == "" {
		http.Error(w, "type and id required", http.StatusBadRequest)
		return
	}

	levels := defaultPermissionLevels()
	if def, ok := h.p.Plugins.ResourceDefByType(resType); ok && len(def.PermissionLevels) > 0 {
		levels = def.PermissionLevels
	}

	var levelChoices []ui.PermissionLevelChoice
	for _, l := range levels {
		levelChoices = append(levelChoices, ui.PermissionLevelChoice{Label: l.Label, Permissions: l.Permissions})
	}

	// Admin mode: show ALL shares, not filtered by owner.
	recs, err := seq.Collect(h.p.Shares.ListByResource(resType, resID))
	if err != nil {
		h.p.Log.Error("admin share panel: list shares", "type", resType, "id", resID, "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	namer, err := newSubjectNamer(h.p, shareSubjectRefs(recs))
	if err != nil {
		h.p.Log.Error("admin share panel: look up sharees", "type", resType, "id", resID, "err", err)
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
		if rec.SubjectType == "PUBLIC" && rec.Token != nil {
			sd.Token = *rec.Token
			sd.LinkPath = "/s/" + sd.Token
		}
		shares = append(shares, sd)
	}

	sessID := auth.SessionIDFromContext(r.Context())
	csrf := auth.CSRFToken(sessID, h.p.CSRFSecret)

	panelID := r.URL.Query().Get("scope")
	if panelID == "" {
		panelID = "share-panel"
	}
	ui.Render(w, r, ui.SharePanel(panelID, resType, resID, csrf, shares, levelChoices))
}

// CreateShareAdmin handles POST from admin share panel. Uses a system admin as owner.
func (h *shareHandler) CreateShareAdmin(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())

	resType := r.FormValue("resource_type")
	resID := r.FormValue("resource_id")
	subjectID := r.FormValue("subject_id")
	permStr := r.FormValue("permissions")

	if resType == "" || resID == "" || subjectID == "" {
		http.Error(w, "missing fields", http.StatusBadRequest)
		return
	}

	var perms uint32 = plugin.PermRead
	if permStr != "" {
		if v, err := strconv.ParseUint(permStr, 10, 32); err == nil && v > 0 {
			perms = uint32(v)
		}
	}

	if _, err := h.p.Shares.Create(u.ID, resType, resID, "USER", subjectID, perms); err != nil {
		h.p.Log.Error("admin share create failed", "err", err)
		httperr.Internal(w, r, err)
		return
	}

	scope := r.FormValue("panel_id")
	r.URL.RawQuery = "type=" + resType + "&id=" + resID + "&scope=" + scope + "&mode=admin"
	h.SharePanelAdmin(w, r)
}

// DeleteShareAdmin handles DELETE from admin share panel (no ownership check).
func (h *shareHandler) DeleteShareAdmin(w http.ResponseWriter, r *http.Request) {
	shareID := r.PathValue("id")
	err := h.p.Shares.Delete(shareID)
	if errors.Is(err, share.ErrNotFound) {
		http.Error(w, "share not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.p.Log.Error("admin delete share", "share", shareID, "err", err)
		httperr.Internal(w, r, err)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(""))
}
