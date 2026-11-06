package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httphdr"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/ui"
)

// volumeBrowserHandler serves the volume browser UI.
type volumeBrowserHandler struct{ handler }

func (h *volumeBrowserHandler) Browse(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	vol, backend, ok := h.open(w, r, u, volID, plugin.PermRead)
	if !ok {
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/"
	}

	if h.serverInternal(vol, dirPath) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	all, err := backend.List(dirPath)
	if err != nil {
		h.p.Log.Error("volume list", "volume", volID, "path", dirPath, "err", err)
		http.Error(w, "Failed to list: "+err.Error(), http.StatusInternalServerError)
		return
	}
	items := make([]volume.FileInfo, 0, len(all))
	hidden := 0
	for _, f := range all {
		if h.serverInternal(vol, f.Path) {
			hidden++
			continue
		}
		items = append(items, f)
	}

	pd := pageDataWithNav(r, "Browse: "+vol.Name, h.p.CSRFSecret, h.p.Plugins)
	back := ui.Crumb{Label: "Storage", URL: "/settings/storage"}
	if u.IsAdmin {
		back = ui.Crumb{Label: "Volumes", URL: "/admin/volumes/" + vol.ID}
	}
	if err := volumeBrowsePage(pd, vol, dirPath, items, hidden, back).Render(r.Context(), w); err != nil {
		h.p.Log.Error("render volume browser", "volume", vol.ID, "err", err)
	}
}

// open checks the user may use the volume as need asks (plugin.PermRead to
// look, plugin.PermWrite to change it) and returns it with its backend. When
// the user may not, open answers the request itself and reports false.
func (h *volumeBrowserHandler) open(w http.ResponseWriter, r *http.Request, u *user.User, volID string, need uint32) (*volume.Volume, volume.Backend, bool) {
	vol, err := h.p.Access.Volumes.GetByID(volID)
	if errors.Is(err, volume.ErrNotFound) {
		http.NotFound(w, r)
		return nil, nil, false
	}
	if err != nil {
		h.p.Log.Error("look up volume", "volume", volID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return nil, nil, false
	}
	perms, err := h.p.Access.Permissions(u.ID, volID)
	if err != nil {
		h.p.Log.Error("volume access check", "volume", volID, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return nil, nil, false
	}
	if perms&need != need {
		http.Error(w, "forbidden", http.StatusForbidden)
		return nil, nil, false
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, "Cannot open this volume: "+err.Error(), http.StatusBadRequest)
		return nil, nil, false
	}
	return vol, backend, true
}

// serverInternal reports whether p, on vol, is one of the server's own files:
// the system database and its journal files, a search index, or a plugin's
// database. They live in the data directory, which the default volume is,
// and deleting one under a running server destroys data. The browser neither
// lists them nor lets them be read, replaced or deleted.
func (h *volumeBrowserHandler) serverInternal(vol *volume.Volume, p string) bool {
	if vol.Scheme() != "file" || h.p.Config.DataDir == "" {
		return false
	}
	root, err := filepath.Abs(vol.URIPath())
	if err != nil {
		return false
	}
	dataDir, err := filepath.Abs(h.p.Config.DataDir)
	if err != nil || root != dataDir {
		return false
	}

	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "" {
		return false
	}
	segs := strings.Split(clean, "/")
	for _, s := range segs {
		if s == ".search" || s == "app_data" {
			return true
		}
	}
	if len(segs) == 1 && strings.HasPrefix(segs[0], ".cog-secret") {
		return true // the master secret (services.loadSecret)
	}
	if strings.HasPrefix(segs[0], "cog-chunk") {
		return true // uploads in progress (compat.ChunkedUploadHandler)
	}
	if len(segs) == 1 {
		// The system database, when it lives in the data directory: where
		// COG_DB puts it, or system.db when nothing says otherwise.
		db := "system.db"
		if h.p.Config.DBPath != "" {
			dbPath, err := filepath.Abs(h.p.Config.DBPath)
			if err != nil || filepath.Dir(dbPath) != dataDir {
				return false
			}
			db = filepath.Base(dbPath)
		}
		// system.db, system.db-wal, system.db-shm, system.db-journal
		if segs[0] == db || strings.HasPrefix(segs[0], db+"-") {
			return true
		}
	}
	return false
}

func (h *volumeBrowserHandler) Download(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	vol, backend, ok := h.open(w, r, u, volID, plugin.PermRead)
	if !ok {
		return
	}

	filePath := r.URL.Query().Get("path")
	if h.serverInternal(vol, filePath) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rc, err := backend.Get(filePath)
	if err != nil {
		h.p.Log.Info("volume download", "volume", volID, "path", filePath, "err", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer rc.Close()

	name := path.Base(filePath)
	ct := mime.TypeByExtension(filepath.Ext(name))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	plugin.UntrustedContent(w, ct)
	w.Header().Set("Content-Disposition", httphdr.Attachment(name))
	if _, err := io.Copy(w, rc); err != nil {
		h.p.Log.Warn("volume download", "volume", volID, "path", filePath, "err", err)
	}
}

func (h *volumeBrowserHandler) Upload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	vol, backend, ok := h.open(w, r, u, volID, plugin.PermWrite)
	if !ok {
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/"
	}

	// Parse multipart form.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "parse form: "+err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	dest := path.Join(dirPath, header.Filename)
	if h.serverInternal(vol, dest) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := backend.Put(dest, file); err != nil {
		h.p.Log.Error("volume upload", "volume", volID, "path", dest, "err", err)
		http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/volumes/"+volID+"/browse?path="+dirPath, http.StatusSeeOther)
}

func (h *volumeBrowserHandler) MkDir(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	vol, backend, ok := h.open(w, r, u, volID, plugin.PermWrite)
	if !ok {
		return
	}

	dirPath := r.URL.Query().Get("path")
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	dest := path.Join(dirPath, name)
	if h.serverInternal(vol, dest) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := backend.Mkdir(dest); err != nil {
		h.p.Log.Error("volume mkdir", "volume", volID, "path", dest, "err", err)
		http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/volumes/"+volID+"/browse?path="+dirPath, http.StatusSeeOther)
}

func (h *volumeBrowserHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	vol, backend, ok := h.open(w, r, u, volID, plugin.PermWrite)
	if !ok {
		return
	}

	filePath := r.URL.Query().Get("path")
	if h.serverInternal(vol, filePath) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := backend.Delete(filePath); err != nil {
		h.p.Log.Error("volume delete", "volume", volID, "path", filePath, "err", err)
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
