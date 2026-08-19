package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFTokenDeterministic(t *testing.T) {
	secret := []byte("test-secret")
	a := CSRFToken("sess1", secret)
	b := CSRFToken("sess1", secret)
	if a != b {
		t.Fatalf("same inputs produced different tokens: %q vs %q", a, b)
	}
}

func TestCSRFTokenDifferentSessions(t *testing.T) {
	secret := []byte("test-secret")
	a := CSRFToken("sess1", secret)
	b := CSRFToken("sess2", secret)
	if a == b {
		t.Fatalf("different sessions produced same token: %q", a)
	}
}

func TestValidateCSRF(t *testing.T) {
	secret := []byte("test-secret")
	token := CSRFToken("sess1", secret)

	r := &http.Request{
		Method:   "POST",
		Header:   http.Header{},
		PostForm: url.Values{"csrf_token": {token}},
	}
	if !ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected valid CSRF token to pass")
	}
}

func TestValidateCSRFInvalid(t *testing.T) {
	secret := []byte("test-secret")

	r := &http.Request{
		Method:   "POST",
		Header:   http.Header{},
		PostForm: url.Values{"csrf_token": {"wrong-token"}},
	}
	if ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected invalid CSRF token to fail")
	}
}

func TestCSRFTokenForPlugin(t *testing.T) {
	secret := []byte("test-secret")
	base := CSRFToken("sess1", secret)
	forPlugin := CSRFTokenFor("sess1", "myplugin", secret)

	if base == forPlugin {
		t.Fatal("plugin-scoped token should differ from base token")
	}

	// Different plugins get different tokens.
	forOther := CSRFTokenFor("sess1", "otherplugin", secret)
	if forPlugin == forOther {
		t.Fatal("different plugins should produce different tokens")
	}
}

func TestValidateCSRFViaHeader(t *testing.T) {
	secret := []byte("test-secret")
	token := CSRFToken("sess1", secret)

	r := &http.Request{
		Method: "POST",
		Header: http.Header{"X-Csrf-Token": {token}},
		Body:   http.NoBody,
	}
	if !ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected CSRF token via header to pass")
	}
}

func TestValidateCSRFEmpty(t *testing.T) {
	secret := []byte("test-secret")

	r := &http.Request{
		Method: "POST",
		Header: http.Header{},
		Body:   http.NoBody,
	}
	if ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected empty CSRF token to fail")
	}
}

func TestValidateCSRFFor(t *testing.T) {
	secret := []byte("test-secret")
	token := CSRFTokenFor("sess1", "myplugin", secret)

	r := &http.Request{
		Method:   "POST",
		Header:   http.Header{},
		PostForm: url.Values{"csrf_token": {token}},
	}
	if !ValidateCSRFFor(r, "sess1", "myplugin", secret) {
		t.Fatal("expected valid plugin CSRF token to pass")
	}

	// Wrong plugin should fail.
	if ValidateCSRFFor(r, "sess1", "wrongplugin", secret) {
		t.Fatal("expected wrong plugin CSRF token to fail")
	}
}

func TestRequireCSRFFor(t *testing.T) {
	secret := []byte("test-secret")
	sessID := "test-session-id"
	pluginID := "myplugin"

	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := CSRF{Secret: secret}.RequireFor(pluginID, ok)

	// Inject session ID into context.
	withSession := func(r *http.Request) *http.Request {
		ctx := context.WithValue(r.Context(), sessionContextKey, sessID)
		return r.WithContext(ctx)
	}

	// GET should pass through without CSRF.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, withSession(httptest.NewRequest("GET", "/", nil)))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d, want %d", rec.Code, http.StatusOK)
	}

	// POST without token should be rejected.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withSession(httptest.NewRequest("POST", "/", nil)))
	if rec.Code == http.StatusOK {
		t.Fatal("POST without CSRF token should be rejected")
	}

	// POST with valid token should pass.
	token := CSRFTokenFor(sessID, pluginID, secret)
	req := httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-CSRF-Token", token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withSession(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with valid CSRF = %d, want %d", rec.Code, http.StatusOK)
	}

	// POST with wrong token should be rejected.
	req = httptest.NewRequest("POST", "/", nil)
	req.Header.Set("X-CSRF-Token", "wrong-token")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, withSession(req))
	if rec.Code == http.StatusOK {
		t.Fatal("POST with wrong CSRF token should be rejected")
	}
}

// Checking the token must not consume the body: a handler downstream (the gRPC
// bridge, which forwards raw bytes) still has to be able to read it.
func TestValidateCSRFLeavesBodyReadable(t *testing.T) {
	secret := []byte("test-secret")
	form := url.Values{"csrf_token": {CSRFToken("sess1", secret)}, "field": {"kept"}}.Encode()

	r := httptest.NewRequest("POST", "/", strings.NewReader(form))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if !ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected valid CSRF token to pass")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != form {
		t.Fatalf("body after CSRF check = %q, want %q", body, form)
	}
}
