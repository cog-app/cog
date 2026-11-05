package web

import (
	"errors"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
)

// signedInSession is one row of the "Where you're signed in" table.
type signedInSession struct {
	auth.Session
	Current bool
}

// Device names the browser and OS from the session's User-Agent, such as
// "Firefox on Linux". It only has to be recognisable, not exact.
func (s signedInSession) Device() string {
	return describeUserAgent(s.UserAgent)
}

func describeUserAgent(ua string) string {
	if ua == "" {
		return "Unknown device"
	}
	// Order matters: Edge and Opera also say Chrome, and Chrome also says
	// Safari.
	browsers := []struct{ token, name string }{
		{"Edg/", "Edge"},
		{"OPR/", "Opera"},
		{"Firefox/", "Firefox"},
		{"Chrome/", "Chrome"},
		{"Safari/", "Safari"},
		{"curl/", "curl"},
	}
	browser := ""
	for _, b := range browsers {
		if strings.Contains(ua, b.token) {
			browser = b.name
			break
		}
	}
	systems := []struct{ token, name string }{
		{"Android", "Android"},
		{"iPhone", "iPhone"},
		{"iPad", "iPad"},
		{"Windows", "Windows"},
		{"Mac OS X", "macOS"},
		{"Macintosh", "macOS"},
		{"CrOS", "ChromeOS"},
		{"Linux", "Linux"},
	}
	system := ""
	for _, s := range systems {
		if strings.Contains(ua, s.token) {
			system = s.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " on " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	// Not a browser we know; the product token is the most useful part.
	if name, _, _ := strings.Cut(ua, " "); name != "" {
		return name
	}
	return "Unknown device"
}

// listSignedInSessions returns the user's sessions, marking the one r came in
// on.
func (h *settingsHandler) listSignedInSessions(r *http.Request, userID string) []signedInSession {
	sessions, err := h.p.Sessions.ListByUser(userID)
	if err != nil {
		h.p.Log.Error("list sessions", "user", userID, "err", err)
		return nil
	}
	current := auth.SessionIDFromContext(r.Context())
	out := make([]signedInSession, len(sessions))
	for i, s := range sessions {
		out[i] = signedInSession{Session: s, Current: current != "" && s.Is(current)}
	}
	return out
}

// RevokeSession signs out one of the user's other sessions.
func (h *settingsHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	err := h.p.Sessions.DeleteByHandle(u.ID, r.PathValue("handle"))
	if errors.Is(err, auth.ErrSessionNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.p.Log.Error("revoke session", "user", u.Username, "err", err)
		http.Error(w, "The session could not be signed out.", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// RevokeOtherSessions signs out every session but the one making the request.
func (h *settingsHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if err := h.p.Sessions.DeleteAllExcept(u.ID, auth.SessionIDFromContext(r.Context())); err != nil {
		h.p.Log.Error("revoke other sessions", "user", u.Username, "err", err)
		setFlashForm(w, "error", "Other sessions could not be signed out.", map[string]string{"form": "sessions"})
	} else {
		setFlashForm(w, "success", "Signed out everywhere else.", map[string]string{"form": "sessions"})
	}
	http.Redirect(w, r, "/settings/security#sessions", http.StatusSeeOther)
}
