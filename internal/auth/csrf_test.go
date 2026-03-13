package auth

import (
	"net/http"
	"net/url"
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
		Method: "POST",
		Header: http.Header{},
		Form:   url.Values{"csrf_token": {token}},
	}
	if !ValidateCSRF(r, "sess1", secret) {
		t.Fatal("expected valid CSRF token to pass")
	}
}

func TestValidateCSRFInvalid(t *testing.T) {
	secret := []byte("test-secret")

	r := &http.Request{
		Method: "POST",
		Header: http.Header{},
		Form:   url.Values{"csrf_token": {"wrong-token"}},
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
		Method: "POST",
		Header: http.Header{},
		Form:   url.Values{"csrf_token": {token}},
	}
	if !ValidateCSRFFor(r, "sess1", "myplugin", secret) {
		t.Fatal("expected valid plugin CSRF token to pass")
	}

	// Wrong plugin should fail.
	if ValidateCSRFFor(r, "sess1", "wrongplugin", secret) {
		t.Fatal("expected wrong plugin CSRF token to fail")
	}
}
