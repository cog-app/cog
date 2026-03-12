package compat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"sync"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
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
type LoginFlowHandler struct {
	mu       sync.Mutex
	pending  map[string]*loginFlowEntry // keyed by poll token
	Users    *user.Store
	AppPWs   *auth.AppPasswordStore
	Sessions *auth.SessionStore
	BaseURL  string
}

func NewLoginFlowHandler(users *user.Store, appPWs *auth.AppPasswordStore, sessions *auth.SessionStore, baseURL string) *LoginFlowHandler {
	return &LoginFlowHandler{
		pending:  make(map[string]*loginFlowEntry),
		Users:    users,
		AppPWs:   appPWs,
		Sessions: sessions,
		BaseURL:  baseURL,
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
		CreatedAt: time.Now(),
	}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"poll": map[string]any{
			"token":    pollToken,
			"endpoint": h.BaseURL + "/index.php/login/v2/poll",
		},
		"login": h.BaseURL + "/index.php/login/v2/grant?token=" + loginToken,
	})
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

	h.mu.Lock()
	entry, ok := h.pending[token]
	if !ok || time.Since(entry.CreatedAt) > loginFlowExpiry {
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
	json.NewEncoder(w).Encode(map[string]any{
		"server":      h.BaseURL,
		"loginName":   entry.LoginName,
		"appPassword": entry.AppPassword,
	})
}

// HandleGrant handles GET and POST /index.php/login/v2/grant
// GET shows a login form; POST validates credentials and grants the flow.
func (h *LoginFlowHandler) HandleGrant(w http.ResponseWriter, r *http.Request) {
	loginToken := r.URL.Query().Get("token")
	if loginToken == "" {
		http.Error(w, "Missing token", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.renderGrantPage(w, loginToken, "")
	case http.MethodPost:
		h.processGrant(w, r, loginToken)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *LoginFlowHandler) processGrant(w http.ResponseWriter, r *http.Request, loginToken string) {
	username := r.FormValue("username")
	password := r.FormValue("password")

	u, err := h.Users.Authenticate(username, password)
	if err != nil {
		h.renderGrantPage(w, loginToken, "Invalid username or password")
		return
	}

	// Find the pending flow by login token
	h.mu.Lock()
	var pollToken string
	var entry *loginFlowEntry
	for pt, e := range h.pending {
		if e.Token == loginToken && !e.Granted {
			pollToken = pt
			entry = e
			break
		}
	}
	if entry == nil {
		h.mu.Unlock()
		http.Error(w, "Login flow expired or not found", http.StatusNotFound)
		return
	}

	// Create an app password for this login flow
	appPW, _, err := h.AppPWs.Create(u.ID, fmt.Sprintf("Login Flow (%s)", time.Now().Format("2006-01-02 15:04")))
	if err != nil {
		h.mu.Unlock()
		http.Error(w, "Failed to create app password", http.StatusInternalServerError)
		return
	}

	entry.LoginName = u.Username
	entry.AppPassword = appPW
	entry.Granted = true
	_ = pollToken // entry is already in the map
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, `<!DOCTYPE html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"></head><body><h1>Login Granted</h1><p>You can close this window and return to your app.</p></body></html>`)
}

func (h *LoginFlowHandler) renderGrantPage(w http.ResponseWriter, token string, errMsg string) {
	tmpl := template.Must(template.New("grant").Parse(`<!DOCTYPE html>
<html>
<head><meta name="viewport" content="width=device-width, initial-scale=1"><title>Cog - Login</title></head>
<body>
<h1>Grant App Access</h1>
{{if .Error}}<p style="color:red">{{.Error}}</p>{{end}}
<form method="POST" action="?token={{.Token}}">
  <label>Username: <input type="text" name="username" required></label><br>
  <label>Password: <input type="password" name="password" required></label><br>
  <button type="submit">Grant Access</button>
</form>
</body>
</html>`))

	w.Header().Set("Content-Type", "text/html")
	tmpl.Execute(w, map[string]string{
		"Token": token,
		"Error": errMsg,
	})
}

// Cleanup removes expired login flows. Call periodically.
func (h *LoginFlowHandler) Cleanup() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for token, entry := range h.pending {
		if time.Since(entry.CreatedAt) > loginFlowExpiry {
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
