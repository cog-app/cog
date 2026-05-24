package compat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/user"
)

const loginFlowExpiry = 10 * time.Minute

type loginFlowEntry struct {
	Token     string
	CreatedAt time.Time
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

	switch r.Method {
	case http.MethodGet:
		h.renderGrantPage(w, loginToken, csrf, u.Username, "")
	case http.MethodPost:
		// Validate CSRF token bound to the current session.
		if r.FormValue("csrf_token") != csrf {
			h.renderGrantPage(w, loginToken, csrf, u.Username, "Invalid CSRF token")
			return
		}
		h.processGrant(w, r, loginToken, u)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
	appPW, _, err := h.AppPWs.Create(u.ID, fmt.Sprintf("Login Flow (%s)", h.Clock().Format("2006-01-02 15:04")))
	if err != nil {
		h.mu.Unlock()
		http.Error(w, "Failed to create app password", http.StatusInternalServerError)
		return
	}

	entry.LoginName = u.Username
	entry.AppPassword = appPW
	entry.Granted = true
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><h1>Login Granted</h1><p>You can close this window and return to your app.</p></body></html>`)
}

var grantTmpl = template.Must(template.New("grant").Parse(`<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cog - Login</title></head>
<body>
<h1>Grant App Access</h1>
{{if .Error}}<p style="color:red">{{.Error}}</p>{{end}}
<p>Signed in as <strong>{{.Username}}</strong>. Approving will create an app password tied to your account for the requesting application.</p>
<form method="POST" action="?token={{.Token}}">
  <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
  <button type="submit">Grant Access</button>
</form>
</body>
</html>`))

func (h *LoginFlowHandler) renderGrantPage(w http.ResponseWriter, token, csrf, username, errMsg string) {
	w.Header().Set("Content-Type", "text/html")
	grantTmpl.Execute(w, map[string]string{
		"Token":     token,
		"CSRFToken": csrf,
		"Username":  username,
		"Error":     errMsg,
	})
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

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
