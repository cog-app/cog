package setup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"

	"git.duckfam.us/jj-software/cog/internal/certs"
	"git.duckfam.us/jj-software/cog/internal/service"
	"git.duckfam.us/jj-software/cog/ui"
)

// Wizard is setup in the browser: a server on this computer's loopback
// address only, with no accounts. What stands in for signing in is a
// random token in the address setup opens, which becomes a cookie; the
// Host header must be the loopback address, so a web page cannot reach it
// through a DNS name it controls, and every form must come from the
// wizard's own pages.
type Wizard struct {
	// Exe is the binary to install.
	Exe string

	addr  string
	token string
	done  chan struct{}
	quit  sync.Once

	mu       sync.Mutex
	installs []Install
	draft    Plan
	problem  error
	job      *job
}

// job is a plan being carried out.
type job struct {
	mu   sync.Mutex
	log  bytes.Buffer
	done bool
	mode string
	res  Result
	err  error
}

func (j *job) Write(b []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.log.Write(b)
}

// cookieName holds the token once the start address has been opened.
const cookieName = "cog_setup"

// Listen starts the wizard on a free loopback port and returns the address
// to open in the browser, which carries the token.
func (w *Wizard) Listen() (net.Listener, string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", fmt.Errorf("listen: %w", err)
	}
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, "", errors.Join(fmt.Errorf("token: %w", err), ln.Close())
	}
	w.token = base64.RawURLEncoding.EncodeToString(b[:])
	w.addr = ln.Addr().String()
	w.done = make(chan struct{})
	installs, err := Find(w.Exe)
	if err != nil {
		return nil, "", errors.Join(err, ln.Close())
	}
	w.installs = installs
	if len(installs) == 0 {
		if w.draft, err = DefaultPlan(service.User, w.Exe); err != nil {
			return nil, "", errors.Join(err, ln.Close())
		}
	}
	return ln, "http://" + w.addr + "/start?token=" + w.token, nil
}

// Serve serves the wizard on ln until the person closes it or ctx ends.
func (w *Wizard) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{Handler: w.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	case <-w.done:
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("stop setup: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler serves the wizard's pages.
func (w *Wizard) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Static))
	mux.HandleFunc("GET /start", w.start)
	mux.HandleFunc("GET /{$}", w.home)
	mux.HandleFunc("POST /choose", w.choose)
	mux.HandleFunc("GET /plan", w.planForm)
	mux.HandleFunc("POST /plan", w.savePlan)
	mux.HandleFunc("GET /review", w.review)
	mux.HandleFunc("POST /apply", w.apply)
	mux.HandleFunc("GET /reset", w.resetForm)
	mux.HandleFunc("POST /reset", w.reset)
	mux.HandleFunc("GET /progress", w.progress)
	mux.HandleFunc("GET /progress/log", w.progressLog)
	mux.HandleFunc("POST /quit", w.quitSetup)
	return w.guard(mux)
}

// guard admits requests to the loopback address that carry the token.
func (w *Wizard) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Host != w.addr {
			http.Error(rw, "setup answers only at "+w.addr, http.StatusMisdirectedRequest)
			return
		}
		if r.URL.Path == "/start" || strings.HasPrefix(r.URL.Path, "/static/") {
			next.ServeHTTP(rw, r)
			return
		}
		c, err := r.Cookie(cookieName)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(w.token)) != 1 {
			http.Error(rw, "Open setup from the address it printed when it started.", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+w.addr {
			http.Error(rw, "Forms are only taken from setup's own pages.", http.StatusForbidden)
			return
		}
		rw.Header().Set("Cache-Control", "no-store")
		rw.Header().Set("X-Frame-Options", "DENY")
		// same-origin, not no-referrer: with no-referrer, browsers send
		// "Origin: null" on form posts, which the check above refuses.
		rw.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(rw, r)
	})
}

func (w *Wizard) start(rw http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(w.token)) != 1 {
		http.Error(rw, "Open setup from the address it printed when it started.", http.StatusForbidden)
		return
	}
	http.SetCookie(rw, &http.Cookie{Name: cookieName, Value: w.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	// The token leaves the address bar and the history.
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

func (w *Wizard) render(rw http.ResponseWriter, r *http.Request, c templ.Component) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), rw); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}

func (w *Wizard) home(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	installs, j := w.installs, w.job
	w.mu.Unlock()
	if j != nil {
		http.Redirect(rw, r, "/progress", http.StatusSeeOther)
		return
	}
	if len(installs) == 0 {
		http.Redirect(rw, r, "/plan", http.StatusSeeOther)
		return
	}
	w.render(rw, r, homePage(installs))
}

// choose picks what to do with an install found, or a new one.
func (w *Wizard) choose(rw http.ResponseWriter, r *http.Request) {
	action, dir, _ := strings.Cut(r.FormValue("action"), ":")
	w.mu.Lock()
	defer w.mu.Unlock()
	w.problem = nil
	if action == "new" {
		p, err := DefaultPlan(service.Scope(r.FormValue("scope")), w.Exe)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if p.Scope == "" {
			p.Scope = service.User
		}
		w.draft = p
		http.Redirect(rw, r, "/plan", http.StatusSeeOther)
		return
	}
	var in *Install
	for i := range w.installs {
		if w.installs[i].Dir == dir {
			in = &w.installs[i]
		}
	}
	if in == nil {
		http.Error(rw, "No install in "+dir, http.StatusBadRequest)
		return
	}
	p, err := in.Plan(w.Exe)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	w.draft = p
	switch action {
	case "reconfigure":
		http.Redirect(rw, r, "/plan", http.StatusSeeOther)
	case "reset":
		w.draft.Mode = ModeResetAdmin
		w.draft.AdminUser = "admin"
		http.Redirect(rw, r, "/reset", http.StatusSeeOther)
	case "uninstall":
		w.draft.Mode = ModeUninstall
		w.startJob(w.draft)
		http.Redirect(rw, r, "/progress", http.StatusSeeOther)
	default:
		http.Error(rw, "unknown action", http.StatusBadRequest)
	}
}

func (w *Wizard) planForm(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	p, prob := w.draft, w.problem
	w.mu.Unlock()
	w.render(rw, r, planPage(p, problemOf(prob), LANAddress(), service.IsAdmin()))
}

func problemOf(err error) Problem {
	var p Problem
	if errors.As(err, &p) {
		return p
	}
	if err != nil {
		return Problem{Message: err.Error()}
	}
	return Problem{}
}

// savePlan takes the form into the draft and, if it holds together, moves
// on to the review.
func (w *Wizard) savePlan(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.draft
	f := r.PostForm
	if p.Mode == ModeInstall {
		p.Scope = service.Scope(f.Get("scope"))
	}
	p.Dir = strings.TrimSpace(f.Get("dir"))
	p.AdminUser = f.Get("admin_user")
	p.AdminPassword = f.Get("admin_password")
	confirm := f.Get("admin_password_confirm")
	p.Access = f.Get("access")
	p.Port = atoi(f.Get("port"))
	p.Domain = f.Get("domain")
	p.Email = strings.TrimSpace(f.Get("email"))
	p.HTTPSPort = atoi(f.Get("https_port"))
	p.Challenge = f.Get("challenge")
	p.HTTPPort = atoi(f.Get("http_port"))
	p.DNSProvider = f.Get("dns_provider")
	p.DNSEnv = map[string]string{}
	if prov, ok := certs.FindDNSProvider(p.DNSProvider); ok {
		for _, v := range prov.Vars {
			p.DNSEnv[v.Name] = strings.TrimSpace(f.Get("dns_env." + v.Name))
		}
	}
	p.Staging = f.Get("staging") == "1"
	p.PublicURL = strings.TrimSpace(f.Get("public_url"))
	p.TrustedProxies = strings.TrimSpace(f.Get("trusted_proxies"))
	p.SSH = f.Get("ssh") == "1"
	p.SSHPort = atoi(f.Get("ssh_port"))
	p.SSHAccountPasswords = f.Get("ssh_account_passwords") == "1"
	p.UpdateCheck = f.Get("update_check") == "1"
	p.UpdateAuto = f.Get("update_auto") == "1"
	p.Firewall = f.Get("firewall") == "1"

	err := p.Validate()
	if err == nil && p.AdminPassword != confirm {
		err = problem("admin_password_confirm", "The passwords do not match.")
	}
	// The password is not kept in the draft for the form to show again.
	if err != nil {
		p.AdminPassword = ""
	}
	w.draft, w.problem = p, err
	if err != nil {
		http.Redirect(rw, r, "/plan#"+problemOf(err).Field, http.StatusSeeOther)
		return
	}
	http.Redirect(rw, r, "/review", http.StatusSeeOther)
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0 // Validate says the port is not one
	}
	return n
}

func (w *Wizard) review(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	p := w.draft
	w.mu.Unlock()
	if err := p.Validate(); err != nil {
		http.Redirect(rw, r, "/plan", http.StatusSeeOther)
		return
	}
	var dns DNSCheck
	if p.Access == AccessInternet {
		dns = CheckDNS(r.Context(), p.Domain)
	}
	w.render(rw, r, reviewPage(p, LANAddress(), dns))
}

func (w *Wizard) apply(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.draft.Validate(); err != nil {
		w.problem = err
		http.Redirect(rw, r, "/plan", http.StatusSeeOther)
		return
	}
	if w.job == nil || w.job.isDone() {
		w.startJob(w.draft)
	}
	http.Redirect(rw, r, "/progress", http.StatusSeeOther)
}

func (w *Wizard) resetForm(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	p, prob := w.draft, w.problem
	w.mu.Unlock()
	w.render(rw, r, resetPage(p, problemOf(prob)))
}

func (w *Wizard) reset(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.draft
	p.Mode = ModeResetAdmin
	p.AdminUser = r.FormValue("admin_user")
	p.AdminPassword = r.FormValue("admin_password")
	err := p.Validate()
	if err == nil && p.AdminPassword != r.FormValue("admin_password_confirm") {
		err = problem("admin_password_confirm", "The passwords do not match.")
	}
	if err != nil {
		p.AdminPassword = ""
		w.draft, w.problem = p, err
		http.Redirect(rw, r, "/reset", http.StatusSeeOther)
		return
	}
	w.draft, w.problem = p, nil
	w.startJob(p)
	http.Redirect(rw, r, "/progress", http.StatusSeeOther)
}

// startJob carries out p in the background. w.mu is held.
func (w *Wizard) startJob(p Plan) {
	j := &job{mode: p.Mode}
	w.job = j
	go func() {
		res, err := Run(context.Background(), p, j, true)
		j.mu.Lock()
		j.done, j.res, j.err = true, res, err
		j.mu.Unlock()
		if err == nil {
			// What was found has changed; the home page lists it anew.
			installs, ferr := Find(w.Exe)
			w.mu.Lock()
			if ferr == nil {
				w.installs = installs
			}
			w.mu.Unlock()
		}
	}()
}

func (j *job) isDone() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done
}

// snapshot is a job's state for a page.
type snapshot struct {
	Log  string
	Done bool
	Mode string
	Res  Result
	Err  error
}

func (w *Wizard) snapshot() (snapshot, bool) {
	w.mu.Lock()
	j := w.job
	w.mu.Unlock()
	if j == nil {
		return snapshot{}, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return snapshot{Log: j.log.String(), Done: j.done, Mode: j.mode, Res: j.res, Err: j.err}, true
}

func (w *Wizard) progress(rw http.ResponseWriter, r *http.Request) {
	s, ok := w.snapshot()
	if !ok {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	w.render(rw, r, progressPage(s))
}

func (w *Wizard) progressLog(rw http.ResponseWriter, r *http.Request) {
	s, ok := w.snapshot()
	if !ok {
		http.NotFound(rw, r)
		return
	}
	w.render(rw, r, progressBody(s))
}

func (w *Wizard) quitSetup(rw http.ResponseWriter, r *http.Request) {
	w.render(rw, r, closedPage())
	if f, ok := rw.(http.Flusher); ok {
		f.Flush()
	}
	w.quit.Do(func() { close(w.done) })
}

// DNSCheck is where a domain points, for the review to warn about.
type DNSCheck struct {
	Addrs   []string
	Private bool // it points into a private network
	Err     error
}

// CheckDNS looks up domain.
func CheckDNS(ctx context.Context, domain string) DNSCheck {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	name := strings.TrimPrefix(domain, "*.")
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil {
		return DNSCheck{Err: err}
	}
	var c DNSCheck
	for _, a := range addrs {
		a = a.Unmap()
		c.Addrs = append(c.Addrs, a.String())
		if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a) {
			c.Private = true
		}
	}
	return c
}

// cgnat is carrier-grade NAT space: an ISP's, not reachable from outside.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")
