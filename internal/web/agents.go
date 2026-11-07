package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/httperr"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

// An agent is an account a user owns (D21): it signs in only with app
// passwords its owner issues, reaches only what is shared with it, and is
// the identity of what it changes. The owner manages it here; an admin sees
// it among the users.
type agentsHandler struct{ handler }

// agentView is an agent as the page shows it.
type agentView struct {
	user.User
	Tokens []auth.AppPassword
}

func (h *agentsHandler) list(ownerID string) ([]agentView, error) {
	agents, err := h.p.Users.ListAgents(ownerID)
	if err != nil {
		return nil, err
	}
	out := make([]agentView, 0, len(agents))
	for _, a := range agents {
		tokens, err := h.p.AppPasswords.ListByUser(a.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, agentView{User: a, Tokens: tokens})
	}
	return out, nil
}

func (h *agentsHandler) Show(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	pd := pageDataWithNav(r, "Agents", h.p.CSRFSecret, h.p.Plugins)
	agents, err := h.list(u.ID)
	if err != nil {
		h.p.Log.Error("list agents", "user", u.Username, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	flash := getFlash(w, r)
	var msg, kind string
	if flash != nil {
		msg, kind = flash.Message, flash.Kind
	}
	ui.Render(w, r, settingsAgentsPage(pd, msg, kind, agents))
}

// Create makes an agent. The name given is joined to the owner's username,
// and the result is the name its changes carry.
func (h *agentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	a, err := h.p.Users.CreateAgent(u.ID, name)
	switch {
	case errors.Is(err, user.ErrInvalidUsername), errors.Is(err, user.ErrUsernameTaken), errors.Is(err, user.ErrUsernameRetired):
		setFlash(w, "error", err.Error())
	case err != nil:
		setFlash(w, "error", "The agent could not be created.", err)
	default:
		setFlash(w, "success", "Agent “"+a.Username+"” created. Share folders, calendars or contacts with it, and issue it a token below.")
	}
	http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
}

// own returns the agent id names when u owns it.
func (h *agentsHandler) own(u *user.User, id string) (*user.User, bool) {
	a, err := h.p.Users.GetByID(id)
	if err != nil || a.OwnerID != u.ID {
		return nil, false
	}
	return a, true
}

// CreateToken issues an agent an app password. The token is shown once, in
// the HTMX response body, and never kept in a cookie or a page a refresh
// could post again. A plain form post creates nothing: a token it made could
// not be shown, and would be a working credential nobody has seen.
func (h *agentsHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	a, ok := h.own(u, r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		setFlash(w, "error", "Tokens are shown once, so they are created only from this page with scripts on.")
		http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		ui.Render(w, r, ui.Alert("error", "A token needs a name."))
		return
	}
	plaintext, _, err := h.p.AppPasswords.Create(a.ID, name)
	if err != nil {
		httperr.Log(r, fmt.Errorf("create token for agent %s: %w", a.ID, err))
		ui.Render(w, r, ui.Alert("error", "The token could not be created."))
		return
	}
	tokens, err := h.p.AppPasswords.ListByUser(a.ID)
	if err != nil {
		httperr.Internal(w, r, fmt.Errorf("list tokens of agent %s: %w", a.ID, err))
		return
	}
	pd := pageDataWithNav(r, "Agents", h.p.CSRFSecret, h.p.Plugins)
	ui.Render(w, r, agentTokenCreated(pd, agentView{User: *a, Tokens: tokens}, plaintext))
}

func (h *agentsHandler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	a, ok := h.own(u, r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	err := h.p.AppPasswords.Delete(r.PathValue("token"), a.ID)
	if errors.Is(err, auth.ErrAppPasswordNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		setFlash(w, "error", "The token could not be revoked.", err)
	} else {
		setFlash(w, "success", "Token revoked.")
	}
	http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
}

// Delete removes an agent with its tokens and the shares made with it.
func (h *agentsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	a, ok := h.own(u, r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err := h.p.DeleteUser(a.ID); err != nil {
		setFlash(w, "error", "The agent could not be deleted.", err)
	} else {
		setFlash(w, "success", "Agent “"+a.Username+"” deleted.")
	}
	http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
}
