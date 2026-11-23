package web

import (
	"context"
	"net/http"
	"time"

	"git.duckfam.us/jj-software/cog/internal/update"
	"git.duckfam.us/jj-software/cog/internal/version"
	"git.duckfam.us/jj-software/cog/ui"
)

// ShowUpdates shows this build's version and, when the server has an
// updater, what it last found.
func (h *adminHandler) ShowUpdates(w http.ResponseWriter, r *http.Request) {
	pd := pageDataWithNav(r, "Updates", h.p.CSRFSecret, h.p.Plugins)
	var flashMsg, flashKind string
	if flash := getFlash(w, r); flash != nil {
		flashMsg, flashKind = flash.Message, flash.Kind
	}
	var st *update.Status
	if h.p.Updates != nil {
		s := h.p.Updates.Status()
		st = &s
	}
	ui.Render(w, r, adminUpdatesPage(pd, version.Get(), st, flashMsg, flashKind))
}

// CheckUpdates looks for a release now.
func (h *adminHandler) CheckUpdates(w http.ResponseWriter, r *http.Request) {
	defer http.Redirect(w, r, "/admin/updates", http.StatusSeeOther)
	if h.p.Updates == nil {
		setFlash(w, "error", "This build is updated by whoever packaged it.")
		return
	}
	rel, err := h.p.Updates.Check(r.Context())
	switch {
	case err != nil:
		setFlash(w, "error", "Could not check for updates: "+err.Error(), err)
	case rel == nil:
		setFlash(w, "success", "Cog "+version.Get()+" is the newest release.")
	default:
		setFlash(w, "success", "Cog "+rel.Version+" is available.")
	}
}

// InstallUpdate installs the release the last check found, in the
// background: the server restarts when it is in place, which would cut
// this request off.
func (h *adminHandler) InstallUpdate(w http.ResponseWriter, r *http.Request) {
	defer http.Redirect(w, r, "/admin/updates", http.StatusSeeOther)
	if h.p.Updates == nil {
		setFlash(w, "error", "This build is updated by whoever packaged it.")
		return
	}
	st := h.p.Updates.Status()
	if st.Latest == nil {
		setFlash(w, "error", "There is no newer release to install. Check again first.")
		return
	}
	if st.Replace != nil {
		setFlash(w, "error", "Cog cannot replace its own binary: "+st.Replace.Error(), st.Replace)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if err := h.p.Updates.Install(ctx); err != nil {
			h.p.Log.Error("install update", "version", st.Latest.Version, "err", err)
		}
	}()
	setFlash(w, "success", "Installing Cog "+st.Latest.Version+". Cog restarts when it is in place; reload this page in a minute.")
}
