package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/plugin"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

type AdminVolumeHandler struct {
	Volumes  *volume.Store
	Registry *plugin.Registry
	Secret   []byte
}

func (h *AdminVolumeHandler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Volumes", h.Secret, h.Registry)
	volumes, _ := h.Volumes.List()
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminVolumesPage(pd, volumes, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminVolumeHandler) NewVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New Volume", h.Secret, h.Registry)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	adminVolumeNewPage(pd, errMsg).Render(r.Context(), w)
}

func (h *AdminVolumeHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	uri := r.FormValue("uri")

	if name == "" || uri == "" {
		setFlash(w, "error", "Name and URI are required.")
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	u := auth.UserFromContext(r.Context())
	_, err := h.Volumes.Create(name, r.FormValue("description"), uri, u.Username, nil, "{}")
	if err != nil {
		setFlash(w, "error", "Failed to create volume: "+err.Error())
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/admin/volumes", http.StatusSeeOther)
}

func (h *AdminVolumeHandler) EditVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Edit Volume", h.Secret, h.Registry)
	id := r.PathValue("id")
	v, err := h.Volumes.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminVolumeEditPage(pd, v, flashMsg, flashKind).Render(r.Context(), w)
}

func (h *AdminVolumeHandler) UpdateVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.Volumes.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	uri := r.FormValue("uri")

	if err := h.Volumes.Update(id, name, description, uri); err != nil {
		setFlash(w, "error", "Failed to update: "+err.Error())
	} else {
		setFlash(w, "success", "Volume updated.")
	}
	http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
}

func (h *AdminVolumeHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Volumes.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
