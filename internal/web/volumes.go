package web

import (
	"io"
	"mime"
	"net/http"
	"path"
	"path/filepath"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

// VolumeBrowserHandler serves the volume browser UI.
type VolumeBrowserHandler struct {
	Access   *volume.Access
	Registry *plugin.Registry
	Secret   []byte
}

func (h *VolumeBrowserHandler) Browse(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	ok, err := h.Access.CanAccess(u.ID, volID)
	if err != nil || !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vol, err := h.Access.Volumes.GetByID(volID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, "Cannot browse this volume: "+err.Error(), http.StatusBadRequest)
		return
	}

	dirPath := r.URL.Query().Get("path")
	if dirPath == "" {
		dirPath = "/"
	}

	items, err := backend.List(dirPath)
	if err != nil {
		http.Error(w, "Failed to list: "+err.Error(), http.StatusInternalServerError)
		return
	}

	pd := pageDataWithNav(r, "Browse: "+vol.Name, h.Secret, h.Registry)
	volumeBrowsePage(pd, vol, dirPath, items).Render(r.Context(), w)
}

func (h *VolumeBrowserHandler) Download(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	ok, _ := h.Access.CanAccess(u.ID, volID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vol, err := h.Access.Volumes.GetByID(volID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filePath := r.URL.Query().Get("path")
	rc, err := backend.Get(filePath)
	if err != nil {
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
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	io.Copy(w, rc)
}

func (h *VolumeBrowserHandler) Upload(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	ok, _ := h.Access.CanAccess(u.ID, volID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vol, err := h.Access.Volumes.GetByID(volID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	if err := backend.Put(dest, file); err != nil {
		http.Error(w, "upload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/volumes/"+volID+"/browse?path="+dirPath, http.StatusSeeOther)
}

func (h *VolumeBrowserHandler) MkDir(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	ok, _ := h.Access.CanAccess(u.ID, volID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vol, err := h.Access.Volumes.GetByID(volID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	dirPath := r.URL.Query().Get("path")
	name := r.FormValue("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}

	dest := path.Join(dirPath, name)
	if err := backend.Mkdir(dest); err != nil {
		http.Error(w, "mkdir failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/volumes/"+volID+"/browse?path="+dirPath, http.StatusSeeOther)
}

func (h *VolumeBrowserHandler) DeleteFile(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	volID := r.PathValue("id")

	ok, _ := h.Access.CanAccess(u.ID, volID)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	vol, err := h.Access.Volumes.GetByID(volID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	backend, err := volume.NewBackend(vol)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	filePath := r.URL.Query().Get("path")
	if err := backend.Delete(filePath); err != nil {
		http.Error(w, "delete failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
