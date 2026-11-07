package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"git.duckfam.us/jj-software/cog/internal/activity"
	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httperr"
)

type activityHandler struct{ handler }

// trayActivityLimit is how many entries the notification tray shows.
const trayActivityLimit = 20

// activityJSON is an activity entry as the notification tray reads it.
type activityJSON struct {
	ID      int64  `json:"id"`
	App     string `json:"app"`
	Type    string `json:"type"`
	Subject string `json:"subject"`
	// Own says the user did this themselves.
	Own bool `json:"own"`
	// Notify marks activity addressed to the user, which the tray counts
	// and may show on the desktop.
	Notify bool      `json:"notify"`
	At     time.Time `json:"at"`
}

// Recent handles GET /api/activity: the user's latest activity, newest
// first. ?filter=notifications keeps what was addressed to them, and
// ?filter=app:<app> one app's; with ?apps=1 the response also lists the apps
// there is activity from, for the tray's filter.
func (h activityHandler) Recent(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	var f activity.Filter
	filter := r.URL.Query().Get("filter")
	if app, ok := strings.CutPrefix(filter, "app:"); ok && app != "" {
		f.App = app
	} else if filter == "notifications" {
		f.Notifications = true
	} else if filter != "" {
		http.Error(w, "unknown filter", http.StatusBadRequest)
		return
	}
	entries, err := h.p.Activities.Recent(u.Username, f, trayActivityLimit)
	if err != nil {
		httperr.Internal(w, r, err)
		return
	}
	out := struct {
		Items []activityJSON `json:"items"`
		Apps  []string       `json:"apps,omitempty"`
	}{Items: make([]activityJSON, len(entries))}
	for i, a := range entries {
		out.Items[i] = activityJSON{
			ID:      a.ID,
			App:     a.App,
			Type:    a.Type,
			Subject: a.Subject,
			Own:     a.ActorID == u.Username,
			Notify:  a.Notify,
			At:      time.Unix(a.CreatedAt, 0).UTC(),
		}
	}
	if r.URL.Query().Get("apps") != "" {
		if out.Apps, err = h.p.Activities.Apps(u.Username); err != nil {
			httperr.Internal(w, r, err)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		h.p.Log.Error("activity: encode", "user", u.Username, "err", err)
	}
}
