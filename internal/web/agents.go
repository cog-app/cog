package web

import (
	"errors"
	"net/http"
	"strings"

	"git.duckfam.us/jj-software/cog/internal/auth"
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

// Create makes an agent. Its username is validated like a person's: it is
// the name its changes carry, and it must not collide with anyone.
func (h *agentsHandler) Create(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	name := strings.TrimSpace(r.FormValue("name"))
	_, err := h.p.Users.CreateAgent(u.ID, name)
	switch {
	case errors.Is(err, user.ErrInvalidUsername), errors.Is(err, user.ErrUsernameTaken), errors.Is(err, user.ErrUsernameRetired):
		setFlash(w, "error", err.Error())
	case err != nil:
		setFlash(w, "error", "The agent could not be created.", err)
	default:
		setFlash(w, "success", "Agent “"+name+"” created. Share folders, calendars or contacts with it, and issue it a token below.")
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

// CreateToken issues an agent an app password. The token is shown once,
// in the page the request returns, never kept in a cookie.
func (h *agentsHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	a, ok := h.own(u, r.PathValue("id"))
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "token"
	}
	plaintext, _, err := h.p.AppPasswords.Create(a.ID, name)
	if err != nil {
		setFlash(w, "error", "The token could not be created.", err)
		http.Redirect(w, r, "/settings/agents", http.StatusSeeOther)
		return
	}
	pd := pageDataWithNav(r, "Agents", h.p.CSRFSecret, h.p.Plugins)
	agents, err := h.list(u.ID)
	if err != nil {
		h.p.Log.Error("list agents", "user", u.Username, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	ui.Render(w, r, settingsAgentsPageWithToken(pd, agents, a.Username, plaintext))
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
