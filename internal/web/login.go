package web

import (
	"net/http"
	"net/url"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/ui"
)

type loginHandler struct{ handler }

// safeNext validates a next-redirect parameter to prevent open redirects.
func safeNext(next string) string {
	if next != "" && strings.HasPrefix(next, "/") && !strings.HasPrefix(next, "//") {
		return next
	}
	return "/"
}

// loginRedirect builds a redirect URL preserving the next parameter.
func loginRedirect(base, next string) string {
	if next != "" {
		return base + "?next=" + url.QueryEscape(next)
	}
	return base
}

func (h *loginHandler) ShowLogin(w http.ResponseWriter, r *http.Request) {
	if auth.UserFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	next := r.URL.Query().Get("next")
	regEnabled := h.p.Settings != nil && h.p.Settings.GetBool(settings.KeyPublicRegistration)
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.LoginPage(errMsg, "", next, regEnabled).Render(r.Context(), w)
}

func (h *loginHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	password := r.FormValue("password")
	next := r.FormValue("next")

	u, err := h.p.Users.Authenticate(username, password)
	if err != nil {
		setFlash(w, "error", "Invalid username or password.")
		http.Redirect(w, r, loginRedirect("/login", next), http.StatusSeeOther)
		return
	}

	sess, err := h.p.Sessions.Create(u.ID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

func (h *loginHandler) HandleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		h.p.Sessions.Delete(cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *loginHandler) ShowRegister(w http.ResponseWriter, r *http.Request) {
	if auth.UserFromContext(r.Context()) != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	if h.p.Settings == nil || !h.p.Settings.GetBool(settings.KeyPublicRegistration) {
		http.NotFound(w, r)
		return
	}
	next := r.URL.Query().Get("next")
	flash := getFlash(w, r)
	var errMsg string
	if flash != nil {
		errMsg = flash.Message
	}
	ui.RegisterPage(errMsg, next).Render(r.Context(), w)
}

func (h *loginHandler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	if h.p.Settings == nil || !h.p.Settings.GetBool(settings.KeyPublicRegistration) {
		http.NotFound(w, r)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")
	next := r.FormValue("next")

	if username == "" || password == "" {
		setFlash(w, "error", "Username and password are required.")
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}
	if password != confirm {
		setFlash(w, "error", "Passwords do not match.")
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	u, err := h.p.Users.Create(username, password)
	if err != nil {
		setFlash(w, "error", "Registration failed: "+err.Error())
		http.Redirect(w, r, loginRedirect("/register", next), http.StatusSeeOther)
		return
	}

	// Create root mount.
	if h.p.Volumes != nil && h.p.Mounts != nil {
		if defVol, err := h.p.Volumes.GetByName("default"); err == nil {
			subpath := u.Username + "/files"
			h.p.Mounts.Create(u.ID, "/", defVol.ID, subpath, false)
		}
	}

	sess, err := h.p.Sessions.Create(u.ID)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}
