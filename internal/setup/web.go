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
	problems Problems
	// reviewing is set once the review page has been seen: a page saved
	// after that goes back to it rather than on to the next question.
	reviewing bool
	job       *job
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
	mux.HandleFunc("GET /step/{id}", w.stepForm)
	mux.HandleFunc("POST /step/{id}", w.saveStep)
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
		http.Redirect(rw, r, "/step/"+Steps[0].ID, http.StatusSeeOther)
		return
	}
	w.render(rw, r, homePage(installs))
}

// choose picks what to do with an install found, or a new one.
func (w *Wizard) choose(rw http.ResponseWriter, r *http.Request) {
	action, dir, _ := strings.Cut(r.FormValue("action"), ":")
	w.mu.Lock()
	defer w.mu.Unlock()
	w.problems = nil
	if action == "new" {
		p, err := DefaultPlan(service.Scope(r.FormValue("scope")), w.Exe)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if p.Scope == "" {
			p.Scope = service.User
		}
		w.draft, w.reviewing = p, false
		http.Redirect(rw, r, "/step/"+Steps[0].ID, http.StatusSeeOther)
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
		// Everything is answered already: start from the summary, whose
		// links open the page for each answer.
		w.reviewing = true
		http.Redirect(rw, r, "/review", http.StatusSeeOther)
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

func (w *Wizard) stepForm(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	p, probs := w.draft, w.problems
	w.mu.Unlock()
	st, i := findStep(p, r.PathValue("id"))
	if i < 0 {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	steps := VisibleSteps(p)
	back := "/"
	if i > 0 {
		back = "/step/" + steps[i-1].ID
	} else if len(w.installs) == 0 {
		back = ""
	}
	var ts TailscaleStatus
	if st.ID == "access" || st.ID == "tailscale" {
		ts = CheckTailscale(r.Context())
	}
	v := stepView{
		Plan: p, Step: st, Index: i, Total: len(steps), Problems: probs,
		Back: back, Reviewing: w.reviewing, LAN: LANAddress(), Admin: service.IsAdmin(), Tailscale: ts,
	}
	w.render(rw, r, stepPage(v))
}

// saveStep takes a page's answers into the draft, then moves on: to the
// page's own problems, to the next question, or (once the review has been
// seen) back to the review, by way of any page the change has left
// unanswered.
func (w *Wizard) saveStep(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.draft
	st, i := findStep(p, r.PathValue("id"))
	if i < 0 {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	extra := readStep(r.Context(), st.ID, r.PostForm, &p)
	probs, err := stepProblems(&p, st, extra)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(probs) > 0 {
		if st.ID == "admin" {
			// A password the person must type again is not kept.
			p.AdminPassword = ""
		}
		w.draft, w.problems = p, probs
		http.Redirect(rw, r, "/step/"+st.ID, http.StatusSeeOther)
		return
	}
	changedAccess := st.ID == "access" && p.Access != w.draft.Access
	w.draft, w.problems = p, nil
	if changedAccess && accessStep(p) != "access" {
		// A new way in has questions of its own.
		http.Redirect(rw, r, "/step/"+accessStep(p), http.StatusSeeOther)
		return
	}
	if w.reviewing {
		next, ok, err := firstStepWithProblems(p)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if ok {
			http.Redirect(rw, r, "/step/"+next.ID, http.StatusSeeOther)
			return
		}
		http.Redirect(rw, r, "/review", http.StatusSeeOther)
		return
	}
	// The page may have changed which pages follow (a different access).
	steps := VisibleSteps(p)
	_, i = findStep(p, st.ID)
	if i+1 < len(steps) {
		http.Redirect(rw, r, "/step/"+steps[i+1].ID, http.StatusSeeOther)
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
	w.reviewing = true
	p := w.draft
	w.mu.Unlock()
	next, ok, err := firstStepWithProblems(p)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if ok {
		http.Redirect(rw, r, "/step/"+next.ID, http.StatusSeeOther)
		return
	}
	var dns DNSCheck
	if p.Access == AccessInternet {
		dns = CheckDNS(r.Context(), p.Domain)
	}
	w.render(rw, r, reviewPage(p, LANAddress(), dns))
}

func (w *Wizard) apply(rw http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(rw, err.Error(), http.StatusBadRequest)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.draft.Scope == service.System && r.PostForm.Has("firewall_shown") {
		w.draft.Firewall = r.FormValue("firewall") == "1"
	}
	if err := w.draft.Validate(); err != nil {
		http.Redirect(rw, r, "/review", http.StatusSeeOther)
		return
	}
	if w.job == nil || w.job.isDone() {
		w.startJob(w.draft)
	}
	http.Redirect(rw, r, "/progress", http.StatusSeeOther)
}

func (w *Wizard) resetForm(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	p, probs := w.draft, w.problems
	w.mu.Unlock()
	w.render(rw, r, resetPage(p, probs))
}

func (w *Wizard) reset(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	p := w.draft
	p.Mode = ModeResetAdmin
	p.AdminUser = r.FormValue("admin_user")
	p.AdminPassword = r.FormValue("admin_password")
	probs, err := p.Problems()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	if p.AdminPassword != r.FormValue("admin_password_confirm") {
		probs = append(probs, Problem{Field: "admin_password_confirm", Message: "The passwords do not match."})
	}
	if len(probs) > 0 {
		p.AdminPassword = ""
		w.draft, w.problems = p, probs
		http.Redirect(rw, r, "/reset", http.StatusSeeOther)
		return
	}
	w.draft, w.problems = p, nil
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
