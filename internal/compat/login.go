package compat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/ui"
)

const loginFlowExpiry = 10 * time.Minute

type loginFlowEntry struct {
	Token     string
	CreatedAt time.Time
	Client    string // who started the flow, named from its User-Agent
	// Filled when user grants access
	LoginName   string
	AppPassword string
	Granted     bool
}

// LoginFlowHandler implements NextCloud Login Flow v2.
//
// Security model: the grant endpoint requires the user be authenticated in the
// browser already (a session cookie). The grant page is a single CSRF-protected
// "Grant" button — it never re-prompts for the password. This means an attacker
// who starts a login flow with their own poll token cannot trick a victim into
// minting an app password against their (the victim's) account, since the
// granted credentials always come from the browser session, not form input.
type LoginFlowHandler struct {
	mu          sync.Mutex
	pending     map[string]*loginFlowEntry // keyed by poll token
	Users       *user.Store
	AppPWs      *auth.AppPasswordStore
	Sessions    *auth.SessionStore
	BaseURL     string
	RateLimiter *ratelimit.Limiter
	Clock       clock.Clock
	CSRFSecret  []byte
}

func NewLoginFlowHandler(users *user.Store, appPWs *auth.AppPasswordStore, sessions *auth.SessionStore, baseURL string, csrfSecret []byte) *LoginFlowHandler {
	return &LoginFlowHandler{
		pending:    make(map[string]*loginFlowEntry),
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

	h.mu.Lock()
	h.pending[pollToken] = &loginFlowEntry{
		Token:     loginToken,
		CreatedAt: h.Clock(),
		Client:    clientName(r.UserAgent()),
	}
	h.mu.Unlock()

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

	// Rate-limit by client IP. NextCloud clients poll once or twice per
	// second; an attacker can spam this to lock contention on the pending
	// map (audit security.md #22). Apply the same limiter used for /login.
	if h.RateLimiter != nil {
		ip := ratelimit.ClientIP(r)
		if h.RateLimiter.IsLimited(ip) {
			http.Error(w, "Rate limited", http.StatusTooManyRequests)
			return
		}
	}

	token := r.FormValue("token")
	if token == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	entry, ok := h.pending[token]
	if !ok || h.Clock().Sub(entry.CreatedAt) > loginFlowExpiry {
		if ok {
			delete(h.pending, token)
		}
		h.mu.Unlock()
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	if !entry.Granted {
		h.mu.Unlock()
		http.Error(w, "Not yet granted", http.StatusNotFound)
		return
	}

	// Granted — remove from pending and return credentials
	delete(h.pending, token)
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"server":      h.BaseURL,
		"loginName":   entry.LoginName,
		"appPassword": entry.AppPassword,
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
		http.Redirect(w, r, "/login?next=/index.php/login/v2/grant?token="+loginToken, http.StatusSeeOther)
		return
	}

	sessID := auth.SessionIDFromContext(r.Context())
	csrf := auth.CSRFToken(sessID, h.CSRFSecret)

	client, ok := h.pendingClient(loginToken)
	if !ok {
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
		if r.URL.Query().Get("error") == "csrf" {
			errMsg = "Your session changed while this page was open. Try again."
		}
		h.renderGrantPage(w, r, ui.LoginFlowGrant{
			Token:     loginToken,
			CSRFToken: csrf,
			Username:  u.Username,
			Client:    client,
			Error:     errMsg,
		})
	case http.MethodPost:
		// Validate CSRF token bound to the current session.
		if r.FormValue("csrf_token") != csrf {
			http.Redirect(w, r, "/index.php/login/v2/grant?token="+url.QueryEscape(loginToken)+"&error=csrf", http.StatusSeeOther)
			return
		}
		if r.FormValue("action") == "cancel" {
			h.cancel(loginToken)
			http.Redirect(w, r, "/index.php/login/v2/done?result=cancelled", http.StatusSeeOther)
			return
		}
		h.processGrant(w, r, loginToken, u)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// pendingClient returns the name of the app waiting on loginToken, and
// whether the flow is still pending.
func (h *LoginFlowHandler) pendingClient(loginToken string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.pending {
		if e.Token == loginToken && !e.Granted && h.Clock().Sub(e.CreatedAt) <= loginFlowExpiry {
			return e.Client, true
		}
	}
	return "", false
}

// cancel drops a pending flow, so the app's polling ends without credentials.
func (h *LoginFlowHandler) cancel(loginToken string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for poll, e := range h.pending {
		if e.Token == loginToken && !e.Granted {
			delete(h.pending, poll)
			return
		}
	}
}

func (h *LoginFlowHandler) processGrant(w http.ResponseWriter, r *http.Request, loginToken string, u *user.User) {
	// Find the pending flow by login token
	h.mu.Lock()
	var entry *loginFlowEntry
	for _, e := range h.pending {
		if e.Token == loginToken && !e.Granted {
			entry = e
			break
		}
	}
	if entry == nil {
		h.mu.Unlock()
		http.Error(w, "Login flow expired or not found", http.StatusNotFound)
		return
	}

	// Create an app password tied to the authenticated browser session's user.
	appPW, _, err := h.AppPWs.Create(u.ID, fmt.Sprintf("%s (%s)", entry.Client, h.Clock().Format("2006-01-02 15:04")))
	if err != nil {
		h.mu.Unlock()
		slog.Error("login flow: create app password", "user", u.Username, "err", err)
		http.Error(w, "Failed to create app password", http.StatusInternalServerError)
		return
	}

	entry.LoginName = u.Username
	entry.AppPassword = appPW
	entry.Granted = true
	h.mu.Unlock()

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
	if len(name) > 40 {
		name = name[:40]
	}
	return name
}

// Cleanup removes expired login flows. Call periodically.
func (h *LoginFlowHandler) Cleanup() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for token, entry := range h.pending {
		if h.Clock().Sub(entry.CreatedAt) > loginFlowExpiry {
			delete(h.pending, token)
		}
	}
}

// StartCleanup runs Cleanup every loginFlowExpiry/2 until ctx is canceled.
// Without this the pending map grows unboundedly if clients abandon their
// polls (audit security.md #56).
func (h *LoginFlowHandler) StartCleanup(ctx context.Context) {
	interval := loginFlowExpiry / 2
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.Cleanup()
			}
		}
	}()
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
