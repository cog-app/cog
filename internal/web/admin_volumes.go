package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

type AdminVolumeHandler struct {
	Volumes *volume.Store
	Secret  []byte
}

func (h *AdminVolumeHandler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Volumes", h.Secret)
	volumes, _ := h.Volumes.List()
	adminVolumesPage(pd, volumes, "", "").Render(r.Context(), w)
}

func (h *AdminVolumeHandler) NewVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "New Volume", h.Secret)
	adminVolumeNewPage(pd, "").Render(r.Context(), w)
}

func (h *AdminVolumeHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "New Volume", h.Secret)
	name := r.FormValue("name")
	uri := r.FormValue("uri")

	if name == "" || uri == "" {
		adminVolumeNewPage(pd, "Name and URI are required.").Render(r.Context(), w)
		return
	}

	u := auth.UserFromContext(r.Context())
	_, err := h.Volumes.Create(name, r.FormValue("description"), uri, u.Username, nil, "{}")
	if err != nil {
		adminVolumeNewPage(pd, "Failed to create volume: "+err.Error()).Render(r.Context(), w)
		return
	}

	http.Redirect(w, r, "/admin/volumes", http.StatusSeeOther)
}

func (h *AdminVolumeHandler) EditVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageData(r, "Edit Volume", h.Secret)
	id := r.PathValue("id")
	v, err := h.Volumes.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	adminVolumeEditPage(pd, v, "", "").Render(r.Context(), w)
}

func (h *AdminVolumeHandler) UpdateVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := h.Volumes.GetByID(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	uri := r.FormValue("uri")

	if err := h.Volumes.Update(id, name, description, uri); err != nil {
		pd := pageData(r, "Edit Volume", h.Secret)
		adminVolumeEditPage(pd, v, "Failed to update: "+err.Error(), "error").Render(r.Context(), w)
		return
	}

	v, _ = h.Volumes.GetByID(id)
	pd := pageData(r, "Edit Volume", h.Secret)
	adminVolumeEditPage(pd, v, "Volume updated.", "success").Render(r.Context(), w)
}

func (h *AdminVolumeHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.Volumes.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
