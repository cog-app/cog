// Package cogtest provides an integration test harness for Cog.
//
// It spins up a full server with an in-memory database, wired through
// an http.Client whose transport calls the handler directly (no network).
package cogtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/server"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// HTTPOpt configures an outgoing request. Pass to Get, Post, Head, etc.
type HTTPOpt func(*reqOpts)

// reqOpts accumulates request configuration from HTTPOpt functions.
type reqOpts struct {
	req        *http.Request
	noRedirect bool
}

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

// Env is the integration test environment.
type Env struct {
	Providers *services.Providers
	Client    *http.Client
	Handler   http.Handler
}

// New creates a fresh test environment with an in-memory DB.
// The returned Client routes requests through the server handler directly.
func New(t testing.TB) *Env {
	t.Helper()

	sysDB, err := db.OpenSystem(":memory:")
	if err != nil {
		t.Fatalf("cogtest: open db: %v", err)
	}
	t.Cleanup(func() { sysDB.Close() })

	// Start with a placeholder URL; replace once we know the real one.
	ts := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(ts.Close)

	cfg := config.Config{
		BaseURL: ts.URL,
	}

	p := services.NewFromDB(cfg, sysDB)
	handler := server.New(p)
	ts.Config.Handler = handler

	return &Env{
		Providers: p,
		Client:    ts.Client(),
		Handler:   handler,
	}
}

// --- Setup helpers ---

// CreateUser creates a test user and returns it.
func (e *Env) CreateUser(t testing.TB, username, password string) *user.User {
	t.Helper()
	u, err := e.Providers.Users.Create(username, password)
	if err != nil {
		t.Fatalf("cogtest: create user %q: %v", username, err)
	}
	return u
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
	if o.noRedirect {
		old := e.Client.CheckRedirect
		e.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
			t.Fatalf("cogtest: unexpected redirect from %s %s", req.Method, req.URL)
			return nil
		}
		defer func() { e.Client.CheckRedirect = old }()
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("cogtest: %s %s: %v", req.Method, req.URL, err)
	}
	return &Response{Response: resp, t: t}
}

func (e *Env) doReq(t testing.TB, method, path string, body io.Reader, opts []HTTPOpt) *Response {
	t.Helper()
	req, _ := http.NewRequest(method, e.Providers.Config.BaseURL+path, body)
	o := &reqOpts{req: req}
	for _, opt := range opts {
		opt(o)
	}
	if o.noRedirect {
		old := e.Client.CheckRedirect
		e.Client.CheckRedirect = func(*http.Request, []*http.Request) error {
			t.Fatalf("cogtest: unexpected redirect from %s %s", method, path)
			return nil
		}
		defer func() { e.Client.CheckRedirect = old }()
	}
	resp, err := e.Client.Do(req)
	if err != nil {
		t.Fatalf("cogtest: %s %s: %v", method, path, err)
	}
	return &Response{Response: resp, t: t}
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
