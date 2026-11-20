package compat

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

// LoginFlowHandler implements NextCloud Login Flow v2.
//
// Security model: the grant endpoint requires the user be authenticated in the
// browser already (a session cookie). The grant page is a single CSRF-protected
// "Grant" button — it never re-prompts for the password. This means an attacker
// who starts a login flow with their own poll token cannot trick a victim into
// minting an app password against their (the victim's) account, since the
// granted credentials always come from the browser session, not form input.
type LoginFlowHandler struct {
	DB       *sql.DB // the system database, which holds pending flows
	Users    *user.Store
	AppPWs   *auth.AppPasswordStore
	Sessions *auth.SessionStore
	BaseURL  string
	// RateLimiter counts polls for flows that do not exist, and refuses a
	// client over it any more answers to them. A live flow's poll is always
	// answered, so an app waiting on its grant is never cut off. It is the
	// server's flow limiter, not its sign-in one: a client refused for
	// wrong passwords may still sign an app in.
	RateLimiter *ratelimit.Limiter
	Clock       clock.Clock
	CSRFSecret  []byte
}

func NewLoginFlowHandler(db *sql.DB, users *user.Store, appPWs *auth.AppPasswordStore, sessions *auth.SessionStore, baseURL string, csrfSecret []byte) *LoginFlowHandler {
	return &LoginFlowHandler{
		DB:         db,
		Users:      users,
		AppPWs:     appPWs,
		Sessions:   sessions,
		BaseURL:    baseURL,
		CSRFSecret: csrfSecret,
	}
}

// HandleInit handles POST /index.php/login/v2
// Returns a poll token and login URL for the client.
func (h *LoginFlowHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Starting a flow needs no credentials and writes a row, so a client
	// may have only so many waiting at once (M10). Counting starts instead
	// would refuse an app its next sign-in for having signed in before.
	ip := ratelimit.ClientIP(r)
	n, err := h.flows().waiting(ip)
	if err != nil {
		slog.Error("login flow: count", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if n >= MaxWaitingFlows {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	pollToken, err := randomHex(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	loginToken, err := randomHex(32)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := h.flows().start(pollToken, loginToken, clientName(r.UserAgent()), ip); err != nil {
		slog.Error("login flow: start", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"poll": map[string]any{
			"token":    pollToken,
			"endpoint": h.BaseURL + "/index.php/login/v2/poll",
		},
		"login": h.BaseURL + "/index.php/login/v2/grant?token=" + loginToken,
	}); err != nil {
		slog.Error("login flow json encode", "err", err)
	}
}

// HandlePoll handles POST /index.php/login/v2/poll
// Client polls this until the user grants access.
func (h *LoginFlowHandler) HandlePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	loginName, appPassword, err := h.flows().collect(token)
	switch {
	case errors.Is(err, errFlowPending):
		http.Error(w, "Not yet granted", http.StatusNotFound)
		return
	case errors.Is(err, errFlowNotFound):
		// NextCloud clients poll once or twice per second. Polls for flows
		// that do not exist are limited by IP (audit security.md #22); a
		// poll for a live flow never is, or an app still polling one that
		// expired would cut off the next one its user grants.
		if h.RateLimiter != nil {
			ip := ratelimit.ClientIP(r)
			limited, err := h.RateLimiter.IsLimited(ip)
			if err != nil {
				slog.Error("login flow: poll limit", "err", err)
				http.Error(w, "Internal error", http.StatusInternalServerError)
				return
			}
			if limited {
				http.Error(w, "Rate limited", http.StatusTooManyRequests)
				return
			}
			h.RateLimiter.RecordFailure(ip)
		}
		http.Error(w, "Not found", http.StatusNotFound)
		return
	case err != nil:
		slog.Error("login flow: poll", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"server":      h.BaseURL,
		"loginName":   loginName,
		"appPassword": appPassword,
	}); err != nil {
		slog.Error("login poll json encode", "err", err)
	}
}

// HandleGrant handles GET and POST /index.php/login/v2/grant.
// Requires the user be authenticated; the grant is bound to the session, not
// to form-submitted credentials.
func (h *LoginFlowHandler) HandleGrant(w http.ResponseWriter, r *http.Request) {
	loginToken := r.URL.Query().Get("token")
	if loginToken == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	u := auth.UserFromContext(r.Context())
	if u == nil {
		// Bounce through /login and come back here with the token preserved.
		http.Redirect(w, r, "/login?next="+url.QueryEscape("/index.php/login/v2/grant?token="+url.QueryEscape(loginToken)), http.StatusSeeOther)
		return
	}

	sessID := auth.SessionIDFromContext(r.Context())
	csrf := auth.CSRFToken(sessID, h.CSRFSecret)

	flow, err := h.flows().pending(loginToken)
	client := flow.Client
	if err != nil && !errors.Is(err, errFlowNotFound) {
		slog.Error("login flow: look up", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if err != nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if err := ui.LoginFlowExpiredPage().Render(r.Context(), w); err != nil {
			slog.Error("login flow: render expired page", "err", err)
		}
		return
	}

	switch r.Method {
	case http.MethodGet:
		var errMsg string
		switch e := r.URL.Query().Get("error"); e {
		case "":
		case "csrf":
			errMsg = "Your session changed while this page was open. Try again."
		default:
			errMsg = e
		}
		agents, err := h.Users.ListAgents(u.ID)
		if err != nil {
			slog.Error("login flow: list agents", "user", u.Username, "err", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		var options []ui.AgentOption
		for _, a := range agents {
			options = append(options, ui.AgentOption{ID: a.ID, Name: a.Username})
		}
		h.renderGrantPage(w, r, ui.LoginFlowGrant{
			Agents:    options,
			Token:     loginToken,
			CSRFToken: csrf,
			Username:  u.Username,
			Client:    client,
			From:      flow.IP,
			When:      ago(h.Clock().Unix() - flow.CreatedAt),
			Error:     errMsg,
		})
	case http.MethodPost:
		// Validate CSRF token bound to the current session.
		if sessID == "" || subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf_token")), []byte(csrf)) != 1 {
			http.Redirect(w, r, "/index.php/login/v2/grant?token="+url.QueryEscape(loginToken)+"&error=csrf", http.StatusSeeOther)
			return
		}
		if r.FormValue("action") == "cancel" {
			if err := h.flows().cancel(loginToken); err != nil {
				slog.Error("login flow: cancel", "err", err)
				http.Error(w, "Internal error", http.StatusInternalServerError)
				return
			}
			http.Redirect(w, r, "/index.php/login/v2/done?result=cancelled", http.StatusSeeOther)
			return
		}
		h.processGrant(w, r, loginToken, u)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// flows is the store of pending flows, on the handler's database and clock.
func (h *LoginFlowHandler) flows() *loginFlowStore {
	return &loginFlowStore{db: h.DB, clock: h.Clock}
}

func (h *LoginFlowHandler) processGrant(w http.ResponseWriter, r *http.Request, loginToken string, u *user.User) {
	flow, err := h.flows().pending(loginToken)
	client := flow.Client
	if errors.Is(err, errFlowNotFound) {
		http.Error(w, "Login flow expired or not found", http.StatusNotFound)
		return
	}
	if err != nil {
		slog.Error("login flow: look up", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	// The password is the user's own, or an agent's they own: the app then
	// signs in as the agent and reaches only what is shared with it.
	principal := u
	switch as := r.FormValue("grant_as"); as {
	case "", "me":
	case "new":
		agent, err := h.Users.CreateAgent(u.ID, strings.TrimSpace(r.FormValue("agent_name")))
		if err != nil {
			http.Redirect(w, r, "/index.php/login/v2/grant?token="+url.QueryEscape(loginToken)+"&error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		principal = agent
	default:
		agent, err := h.Users.GetByID(as)
		if err != nil || agent.OwnerID != u.ID {
			http.Error(w, "not your agent", http.StatusForbidden)
			return
		}
		principal = agent
	}

	// Create an app password tied to the principal.
	appPW, ap, err := h.AppPWs.Create(principal.ID, fmt.Sprintf("%s (%s)", client, h.Clock().Format("2006-01-02 15:04")))
	if err != nil {
		slog.Error("login flow: create app password", "user", principal.Username, "err", err)
		http.Error(w, "Failed to create app password", http.StatusInternalServerError)
		return
	}
	if err := h.flows().grant(loginToken, principal.Username, appPW); err != nil {
		// Granted twice at once, or expired meanwhile: the password minted
		// for it would reach nobody.
		if derr := h.AppPWs.Delete(ap.ID, principal.ID); derr != nil {
			slog.Error("login flow: remove unused app password", "user", u.Username, "err", derr)
		}
		if errors.Is(err, errFlowNotFound) {
			http.Error(w, "Login flow expired or not found", http.StatusNotFound)
			return
		}
		slog.Error("login flow: grant", "user", u.Username, "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/index.php/login/v2/done?result=granted", http.StatusSeeOther)
}

// HandleDone handles GET /index.php/login/v2/done: the page the browser lands
// on once the user has granted or cancelled, telling them to go back to the app.
func (h *LoginFlowHandler) HandleDone(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	granted := r.URL.Query().Get("result") == "granted"
	if err := ui.LoginFlowDonePage(granted).Render(r.Context(), w); err != nil {
		slog.Error("login flow: render done page", "err", err)
	}
}

func (h *LoginFlowHandler) renderGrantPage(w http.ResponseWriter, r *http.Request, g ui.LoginFlowGrant) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := ui.LoginFlowGrantPage(g).Render(r.Context(), w); err != nil {
		slog.Error("login flow: render grant page", "err", err)
	}
}

// clientName turns a login-flow client's User-Agent into a name to show the
// user. Nextcloud's clients identify themselves; anything else is shown as
// its product token, which is what a person would recognise if anything.
// ago says how long ago something happened, coarsely: the grant page shows
// when a flow was started so a user can tell their own from a stale link.
func ago(seconds int64) string {
	switch {
	case seconds < 60:
		return "just now"
	case seconds < 120:
		return "a minute ago"
	default:
		return fmt.Sprintf("%d minutes ago", seconds/60)
	}
}

// clientName is what an app password made by a login flow is called: the
// app, from its User-Agent, and the device when the User-Agent's comment
// starts with one ("CogFeed/1.0 (Pixel 8; Android 15)" is "CogFeed on Pixel
// 8"), so a user with the app on two devices can tell the passwords apart.
// A browser's comment names a platform, not a device, and is left out.
func clientName(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case ua == "":
		return "An app"
	case strings.Contains(l, "nextcloud-android"):
		return "Nextcloud for Android"
	case strings.Contains(l, "nextcloud-ios"):
		return "Nextcloud for iOS"
	case strings.Contains(l, "mirall") || strings.Contains(l, "nextcloud"):
		return "Nextcloud desktop client"
	}
	name := ua
	if i := strings.IndexAny(name, " ("); i > 0 {
		name = name[:i]
	}
	if i := strings.IndexByte(name, '/'); i > 0 {
		name = name[:i]
	}
	name = truncate(name, 40)
	if name == "Mozilla" {
		return name
	}
	if _, after, ok := strings.Cut(ua, "("); ok {
		comment := after
		if end := strings.IndexAny(comment, ";)"); end >= 0 {
			comment = comment[:end]
		}
		if device := truncate(strings.TrimSpace(comment), 40); device != "" {
			return name + " on " + device
		}
	}
	return name
}

// truncate cuts s to at most n bytes, on a rune boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
