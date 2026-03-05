package web

import (
	"net/http"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

type adminVolumeHandler struct{ handler }

func (h *adminVolumeHandler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Volumes", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var flashMsg, flashKind string
	if flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	adminVolumesPage(pd, h.p.Volumes.List(), flashMsg, flashKind).Render(r.Context(), w)
}

func (h *adminVolumeHandler) NewVolumeForm(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "New Volume", h.p.CSRFSecret, h.p.Plugins)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	adminVolumeNewPage(pd, errMsg).Render(r.Context(), w)
}

func (h *adminVolumeHandler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	uri := r.FormValue("uri")

	if name == "" || uri == "" {
		setFlash(w, "error", "Name and URI are required.")
		http.Redirect(w, r, "/admin/volumes/new", http.StatusSeeOther)
		return
	}

	u := auth.UserFromContext(r.Context())
	_, err := h.p.Volumes.Create(name, r.FormValue("description"), uri, u.Username, nil, "{}")
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

func (h *adminVolumeHandler) UpdateVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := h.p.Volumes.GetByID(id); err != nil {
		http.NotFound(w, r)
		return
	}

	name := r.FormValue("name")
	description := r.FormValue("description")
	uri := r.FormValue("uri")

	if err := h.p.Volumes.Update(id, name, description, uri); err != nil {
		setFlash(w, "error", "Failed to update: "+err.Error())
	} else {
		setFlash(w, "success", "Volume updated.")
	}
	http.Redirect(w, r, "/admin/volumes/"+id, http.StatusSeeOther)
}

func (h *adminVolumeHandler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.p.Volumes.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
