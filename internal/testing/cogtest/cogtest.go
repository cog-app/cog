// Package cogtest provides an integration test harness for Cog.
//
// It spins up a full server on a throwaway data directory, wired through
// an http.Client whose transport calls the handler directly (no network).
package cogtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"log/slog"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/logbuf"
	"git.duckfam.us/jj-software/cog/internal/maildev"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/internal/web"
	"git.duckfam.us/jj-software/cog/plugin"
)

// HTTPOpt configures an outgoing request. Pass to Get, Post, Head, etc.
type HTTPOpt func(*reqOpts)

// reqOpts accumulates request configuration from HTTPOpt functions.
type reqOpts struct {
	req          *http.Request
	noRedirect   bool
	stopRedirect bool
}

// errStopRedirect is a sentinel error returned by CheckRedirect
// to stop following redirects while still capturing the response.
var errStopRedirect = fmt.Errorf("cogtest: stop redirect")

// Auth sets the Authorization header.
func Auth(value string) HTTPOpt {
	return func(o *reqOpts) { o.req.Header.Set("Authorization", value) }
}

// Header sets a single header key/value pair.
func Header(key, value string) HTTPOpt {
	return func(o *reqOpts) { o.req.Header.Set(key, value) }
}

// NoRedirect causes the request to fail the test on any redirect.
var NoRedirect HTTPOpt = func(o *reqOpts) { o.noRedirect = true }

// StopRedirect stops following redirects and returns the redirect response.
var StopRedirect HTTPOpt = func(o *reqOpts) { o.stopRedirect = true }

// Env is the integration test environment.
type Env struct {
	Providers *services.Providers
	Client    *http.Client
	Clock     *clock.TestClock
	Mailbox   *maildev.Mailbox // captured emails sent during tests
	http.Handler
}

// Now returns the current virtual time from the test clock.
func (e *Env) Now() time.Time { return e.Clock.Now() }

// Tests run in UTC. A time rendered into a page otherwise depends on the
// machine's timezone, so a snapshot golden would agree only with developers
// sharing that offset, and never with CI.
//
// TZ covers everything this process starts — the browser, which formats dates
// itself, and plugin subprocesses, which render their own pages. The
// assignment covers this process, whose time.Local may already have been
// resolved from the old TZ before this runs.
//
// COG_SANDBOX=off is set for the same reason: the admin pages warn when
// converters run unsandboxed, and whether they can depends on the machine.
func init() {
	os.Setenv("TZ", "UTC")
	os.Setenv("COG_SANDBOX", "off")
	time.Local = time.UTC
}

// New creates a fresh test environment with its own DB and temp data dir.
// The returned Client routes requests through the server handler directly.
func New(t testing.TB, plugins ...plugin.Plugin) *Env {
	t.Helper()

	tc := clock.NewTest(time.Date(2026, time.March, 14, 6, 0, 0, 0, time.UTC))
	clockHook := db.ClockHook(func() int64 { return tc.Now().Unix() })

	// The manager is built before the system database so the database can be
	// opened with its update hook, as the server does.
	dataDir := t.TempDir()
	// Logs go through the same tee the server uses, so the ring buffer behind
	// the admin log viewer is filled in tests too — including whatever a plugin
	// subprocess writes to its stdout or stderr.
	logBuf := logbuf.New(logbuf.DefaultCapacity)
	log := slog.New(logbuf.NewTeeHandler(slog.NewTextHandler(t.Output(), nil), logBuf))
	mgr := search.NewManager(log, dataDir)

	// The system database is a file in the temp data directory, the same as the
	// server's and the same as every per-user plugin database the harness
	// already creates. It gives WAL journalling and a working busy timeout, so
	// two connections writing at once queue instead of failing.
	//
	// A plain path, not a file: URI: t.TempDir() builds the directory name out
	// of the test's name, which may contain characters a URI reads as syntax.
	sysDB, err := db.OpenWithHooks(filepath.Join(dataDir, "system.db"),
		clockHook, db.UpdateHook(mgr.SystemHook()))
	if err != nil {
		t.Fatalf("cogtest: open db: %v", err)
	}
	if err := db.Migrate(sysDB); err != nil {
		sysDB.Close()
		t.Fatalf("cogtest: migrate db: %v", err)
	}
	t.Cleanup(func() { sysDB.Close() })

	// Start with a placeholder URL; replace once we know the real one.
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	cfg := config.Config{
		BaseURL:               ts.URL,
		DataDir:               dataDir,
		DisableProcessPlugins: true,
		PluginOrigin:          os.Getenv("COG_PLUGIN_ORIGIN"),
	}

	p, err := services.NewFromDB(log, cfg, sysDB, tc.Clock(), plugins...)
	if err == nil {
		// Tests choose short passwords and run offline; a test of the
		// policy sets it back.
		err = p.Settings.SetMany(map[string]string{
			settings.KeyPasswordMinLength:   "1",
			settings.KeyPasswordBreachCheck: "false",
		})
	}
	if err != nil {
		t.Fatalf("cogtest: providers: %v", err)
	}
	p.LogBuffer = logBuf

	// Override unixepoch() in all per-user plugin DBs too.
	p.Plugins.SetConnectHooks(clockHook)

	// Wire search exactly as the server does. Anything the harness leaves out
	// here is a path no test can reach.
	mgr.SetSystemDB(sysDB)
	services.WireSearch(log, p, mgr)

	// Set up email capture via hook (no real SMTP needed).
	mbox := maildev.New()
	p.Settings.Set(settings.KeySMTPHost, "localhost")
	p.Settings.SetInt(settings.KeySMTPPort, 25)
	p.Settings.Set(settings.KeySMTPFrom, "test@localhost")
	p.EmailHook = mbox.CaptureHook()

	handler := web.New(p)

	// Start search after plugins are registered.
	if err := p.StartSearch(context.Background()); err != nil {
		t.Fatalf("cogtest: start search: %v", err)
	}
	// Cleanups run last-registered-first, so these are in the reverse of the
	// order they happen: the plugin registry is stopped after the search
	// manager, matching services.Shutdown. Closing the databases first leaves
	// the indexer querying handles that are already closed.
	//
	// Stopping the registry also terminates external gRPC plugin
	// subprocesses; without it the test exits with child stdout/stderr pipes
	// still open and the go test runner waits on them.
	t.Cleanup(func() { p.Plugins.Stop() })
	t.Cleanup(func() { mgr.Stop() })
	ts.Config.Handler = handler

	return &Env{
		Providers: p,
		Client:    ts.Client(),
		Clock:     tc,
		Mailbox:   mbox,
		Handler:   handler,
	}
}

// WithCookieJar installs an empty cookie jar on the env's HTTP client so
// cookies (e.g., session cookies) persist across requests in a test. Without
// this, BasicAuth-driven middleware-created sessions would pollute later
// requests in the same test that intend to switch users via BasicAuth.
// Call this only in tests that need a stateful browser-like session.
func (e *Env) WithCookieJar(t testing.TB) *Env {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cogtest: cookiejar: %v", err)
	}
	e.Client.Jar = jar
	return e
}

// --- Setup helpers ---

// User wraps a user with pre-built auth for convenient test usage.
type User struct {
	*user.User
	e    *Env
	Auth HTTPOpt
}

func (u *User) Seed(t testing.TB) *User {
	require.NoError(t, u.e.Providers.SeedPlugins(u.Username))
	return u
}

// CreateUser creates a test user and returns a TestUser with embedded auth.
func (e *Env) CreateUser(t testing.TB, username, password string) *User {
	t.Helper()
	u, err := e.Providers.Users.Create(username, password)
	if err != nil {
		t.Fatalf("cogtest: create user %q: %v", username, err)
	}
	return &User{
		User: u,
		e:    e,
		Auth: BasicAuth(username, password),
	}
}

// --- HTTP helpers ---

// Get performs a GET request.
func (e *Env) Get(t testing.TB, path string, opts ...HTTPOpt) *Response {
	t.Helper()
	return e.doReq(t, "GET", path, nil, opts)
}

// PostForm performs a POST request with form data.
func (e *Env) PostForm(t testing.TB, path string, form url.Values, opts ...HTTPOpt) *Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
		opts = append([]HTTPOpt{Header("Content-Type", "application/x-www-form-urlencoded")}, opts...)
	}
	return e.doReq(t, "POST", path, body, opts)
}

// Post performs a POST request with no body.
func (e *Env) Post(t testing.TB, path string, opts ...HTTPOpt) *Response {
	t.Helper()
	return e.doReq(t, "POST", path, nil, opts)
}

// Head performs a HEAD request.
func (e *Env) Head(t testing.TB, path string, opts ...HTTPOpt) *Response {
	t.Helper()
	return e.doReq(t, "HEAD", path, nil, opts)
}

// Do performs an arbitrary request.
func (e *Env) Do(t testing.TB, req *http.Request, opts ...HTTPOpt) *Response {
	t.Helper()
	o := &reqOpts{req: req}
	for _, opt := range opts {
		opt(o)
	}
	if o.noRedirect || o.stopRedirect {
		old := e.Client.CheckRedirect
		if o.noRedirect {
			e.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
				t.Fatalf("cogtest: unexpected redirect from %s %s", req.Method, req.URL)
				return nil
			}
		} else {
			e.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			}
		}
		defer func() { e.Client.CheckRedirect = old }()
	}
	resp, err := e.Client.Do(req)
	if err != nil && !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("cogtest: %s %s: %v", req.Method, req.URL, err)
	}
	return &Response{Response: resp, t: t}
}

func (e *Env) doReq(t testing.TB, method, path string, body io.Reader, opts []HTTPOpt) *Response {
	t.Helper()
	req, err := http.NewRequest(method, e.Providers.Config.BaseURL+path, body)
	if err != nil {
		t.Fatalf("cogtest: %s %s: %v", method, path, err)
	}
	return e.Do(t, req, opts...)
}

// PostJSON performs a POST request with a JSON body.
func (e *Env) PostJSON(t testing.TB, path string, body any, opts ...HTTPOpt) *Response {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("cogtest: marshal JSON: %v", err)
	}
	opts = append([]HTTPOpt{Header("Content-Type", "application/json")}, opts...)
	return e.doReq(t, "POST", path, strings.NewReader(string(data)), opts)
}

// PostText performs a POST request with a plain text body.
func (e *Env) PostText(t testing.TB, path string, data string, opts ...HTTPOpt) *Response {
	t.Helper()
	opts = append([]HTTPOpt{Header("Content-Type", "text/plain")}, opts...)
	return e.doReq(t, "POST", path, strings.NewReader(string(data)), opts)
}

// Put performs a PUT request with a body.
func (e *Env) Put(t testing.TB, path string, body io.Reader, opts ...HTTPOpt) *Response {
	t.Helper()
	return e.doReq(t, "PUT", path, body, opts)
}

// Delete performs a DELETE request.
func (e *Env) Delete(t testing.TB, path string, opts ...HTTPOpt) *Response {
	t.Helper()
	return e.doReq(t, "DELETE", path, nil, opts)
}

// NewRequest creates an http.Request for use with Do().
func NewRequest(t testing.TB, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("cogtest: new request: %v", err)
	}
	return req
}

// BasicAuth returns an HTTPOpt that sets Basic authentication.
func BasicAuth(username, password string) HTTPOpt {
	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth(username, password)
	return Auth(req.Header.Get("Authorization"))
}

// --- Response ---

// Response wraps an http.Response with assertion and decoding helpers.
type Response struct {
	*http.Response
	body     string
	bodyRead bool
	t        testing.TB
}

// Status asserts the response has the given status code.
// On failure it reads and reports the body. Returns r for chaining.
func (r *Response) Status(want int) *Response {
	r.t.Helper()
	if r.StatusCode != want {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		r.t.Fatalf("%s %s = %d, want %d\nbody: %s",
			r.Request.Method, r.Request.URL.Path, r.StatusCode, want, body)
	}
	return r
}

// JSON decodes the response body as JSON into v, then closes the body.
// Returns r for chaining.
func (r *Response) JSON(v any) *Response {
	r.t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		r.t.Fatalf("cogtest: decode JSON from %s: %v", r.Request.URL.Path, err)
	}
	return r
}

// ReadBody reads and returns the full response body, then closes it.
func (r *Response) ReadBody() string {
	if r.bodyRead {
		return r.body
	}
	r.t.Helper()
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		r.t.Fatalf("cogtest: read body from %s: %v", r.Request.URL.Path, err)
	}
	r.bodyRead = true
	r.body = string(b)
	return r.body
}

// Contains asserts the response body contains the given substring.
// Returns r for chaining.
func (r *Response) Contains(substr string) *Response {
	r.t.Helper()
	body := r.ReadBody()
	if !strings.Contains(body, substr) {
		r.t.Fatalf("%s %s body does not contain %q\nbody: %s",
			r.Request.Method, r.Request.URL.Path, substr, body)
	}
	return r
}

// NotContains asserts the response body does not contain the given substring.
// Returns r for chaining.
func (r *Response) NotContains(substr string) *Response {
	r.t.Helper()
	body := r.ReadBody()
	if strings.Contains(body, substr) {
		r.t.Fatalf("%s %s body unexpectedly contains %q\nbody: %s",
			r.Request.Method, r.Request.URL.Path, substr, body)
	}
	return r
}

// Equals asserts the response body equals the given string exactly.
// Returns r for chaining.
func (r *Response) Equals(want string) *Response {
	r.t.Helper()
	body := r.ReadBody()
	if body != want {
		r.t.Fatalf("%s %s body mismatch\nwant: %q\n got: %q",
			r.Request.Method, r.Request.URL.Path, want, body)
	}
	return r
}
