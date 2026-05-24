package auth

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cogplugin "git.duckfam.us/jj-software/cog/plugin"
)

// --- sanitizeReferer (open-redirect guard) ---

func TestSanitizeReferer_SameOrigin(t *testing.T) {
	got := sanitizeReferer("https://cog.example.com/settings/profile", "cog.example.com")
	if got != "/settings/profile" {
		t.Fatalf("got %q, want /settings/profile", got)
	}
}

func TestSanitizeReferer_SameOriginWithQuery(t *testing.T) {
	got := sanitizeReferer("https://cog.example.com/page?x=1&y=2", "cog.example.com")
	if got != "/page?x=1&y=2" {
		t.Fatalf("got %q, want /page?x=1&y=2", got)
	}
}

func TestSanitizeReferer_CrossOriginRejected(t *testing.T) {
	got := sanitizeReferer("https://evil.example.com/phish", "cog.example.com")
	if got != "" {
		t.Fatalf("cross-origin referer leaked: %q", got)
	}
}

func TestSanitizeReferer_EmptyReferer(t *testing.T) {
	if got := sanitizeReferer("", "cog.example.com"); got != "" {
		t.Fatalf("empty referer returned %q", got)
	}
}

func TestSanitizeReferer_Malformed(t *testing.T) {
	if got := sanitizeReferer("::not-a-url", "cog.example.com"); got != "" {
		t.Fatalf("malformed referer returned %q", got)
	}
}

func TestSanitizeReferer_NoHost(t *testing.T) {
	// A relative path doesn't satisfy same-origin (no host to compare).
	if got := sanitizeReferer("/relative/path", "cog.example.com"); got != "" {
		t.Fatalf("relative referer returned %q", got)
	}
}

// --- csrfReject end-to-end ---

func TestRequireCSRF_RejectRedirectsSameOrigin(t *testing.T) {
	secret := []byte("test-secret")
	h := RequireCSRF(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/foo", nil)
	req.Host = "cog.example.com"
	req.Header.Set("Referer", "https://cog.example.com/settings/security")
	// Inject a sessionID via context so the missing CSRF token (not session)
	// is the rejection reason.
	ctx := context.WithValue(req.Context(), sessionContextKey, "sess1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	loc := rec.Header().Get("Location")
	if loc != "/settings/security" {
		t.Fatalf("redirect = %q, want /settings/security", loc)
	}
}

func TestRequireCSRF_RejectIgnoresCrossOriginReferer(t *testing.T) {
	secret := []byte("test-secret")
	h := RequireCSRF(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/foo", nil)
	req.Host = "cog.example.com"
	req.Header.Set("Referer", "https://evil.example.com/phish")
	ctx := context.WithValue(req.Context(), sessionContextKey, "sess1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	loc := rec.Header().Get("Location")
	if loc == "https://evil.example.com/phish" {
		t.Fatal("open-redirect: cross-origin Referer used as redirect target")
	}
	// Should fall back to the request path.
	if loc != "/foo" {
		t.Fatalf("redirect = %q, want /foo", loc)
	}
}

func TestRequireCSRF_HTMXRejectionGives403(t *testing.T) {
	secret := []byte("test-secret")
	h := RequireCSRF(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/foo", nil)
	req.Header.Set("HX-Request", "true")
	ctx := context.WithValue(req.Context(), sessionContextKey, "sess1")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if rec.Header().Get("HX-Refresh") != "true" {
		t.Fatal("expected HX-Refresh: true on HTMX CSRF rejection")
	}
}

func TestRequireCSRF_GETPassesThrough(t *testing.T) {
	secret := []byte("test-secret")
	called := false
	h := RequireCSRF(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/foo", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !called {
		t.Fatal("GET should pass through CSRF middleware")
	}
}

func TestRequireCSRF_MissingSessionRejected(t *testing.T) {
	secret := []byte("test-secret")
	h := RequireCSRF(secret, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No session in context; even with a "valid" token, reject.
	req := httptest.NewRequest("POST", "/foo", nil)
	req.Header.Set("X-CSRF-Token", "anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("expected rejection when session ID is missing")
	}
}

// --- HashAtRest / DerivedKey ---

func TestDerivedKey_DifferentLabelsProduceDifferentKeys(t *testing.T) {
	master := []byte("test-master-secret-for-hkdf-derivation-tests")
	k1 := DerivedKey(master, "label-one")
	k2 := DerivedKey(master, "label-two")
	if bytes.Equal(k1, k2) {
		t.Fatal("different labels must produce different keys")
	}
	if len(k1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(k1))
	}
}

func TestDerivedKey_Deterministic(t *testing.T) {
	master := []byte("same-master")
	k1 := DerivedKey(master, "label")
	k2 := DerivedKey(master, "label")
	if !bytes.Equal(k1, k2) {
		t.Fatal("same master+label must produce the same key")
	}
}

func TestDerivedKey_DifferentMastersDiverge(t *testing.T) {
	k1 := DerivedKey([]byte("master-a"), "label")
	k2 := DerivedKey([]byte("master-b"), "label")
	if bytes.Equal(k1, k2) {
		t.Fatal("different masters must produce different keys")
	}
}

func TestHashAtRest_Deterministic(t *testing.T) {
	master := []byte("test-secret")
	a := HashAtRest(master, "label", "token-123")
	b := HashAtRest(master, "label", "token-123")
	if a != b {
		t.Fatalf("hashes differ for same input")
	}
}

func TestHashAtRest_DifferentLabelsIsolate(t *testing.T) {
	master := []byte("test-secret")
	a := HashAtRest(master, "label-a", "shared-token")
	b := HashAtRest(master, "label-b", "shared-token")
	if a == b {
		t.Fatal("different labels must produce different hashes for same token")
	}
}

func TestHashAtRest_DifferentTokensIsolate(t *testing.T) {
	master := []byte("test-secret")
	a := HashAtRest(master, "label", "token1")
	b := HashAtRest(master, "label", "token2")
	if a == b {
		t.Fatal("different tokens must produce different hashes")
	}
}

func TestHashAtRest_HexLengthIsSHA256(t *testing.T) {
	got := HashAtRest([]byte("k"), "l", "t")
	if len(got) != 64 {
		t.Fatalf("expected 64-char hex (SHA256), got %d (%q)", len(got), got)
	}
}

// --- encryptSecret / decryptSecret ---

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	key := DerivedKey([]byte("master"), "test")
	plaintext := []byte("secret data")
	ct, err := encryptSecret(plaintext, key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptSecret(ct, key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Fatalf("got %q, want %q", got, plaintext)
	}
}

func TestEncryptDecrypt_DifferentKeyFails(t *testing.T) {
	k1 := DerivedKey([]byte("master"), "purpose-a")
	k2 := DerivedKey([]byte("master"), "purpose-b")
	ct, err := encryptSecret([]byte("data"), k1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decryptSecret(ct, k2); err == nil {
		t.Fatal("expected decrypt to fail with different key")
	}
}

func TestEncryptSecret_NonceIsRandom(t *testing.T) {
	key := DerivedKey([]byte("master"), "test")
	a, _ := encryptSecret([]byte("plaintext"), key)
	b, _ := encryptSecret([]byte("plaintext"), key)
	if a == b {
		t.Fatal("encryption must produce different ciphertexts (random nonce)")
	}
}

func TestDecryptSecret_BadCiphertext(t *testing.T) {
	key := DerivedKey([]byte("master"), "test")
	if _, err := decryptSecret("not-base64!@#", key); err == nil {
		t.Fatal("expected base64 decode error")
	}
}

func TestDecryptSecret_TooShortCiphertext(t *testing.T) {
	key := DerivedKey([]byte("master"), "test")
	// "AA==" is 1 byte after base64 — shorter than GCM nonce (12 bytes).
	if _, err := decryptSecret("AA==", key); err == nil {
		t.Fatal("expected error on too-short ciphertext")
	}
}

func TestEncryptSecret_BadKeyLength(t *testing.T) {
	if _, err := encryptSecret([]byte("data"), []byte{1, 2, 3}); err == nil {
		t.Fatal("expected error for wrong AES key length")
	}
}

// --- CookieConfig ---

func TestCookieConfig_SecureFromHTTPS(t *testing.T) {
	c := cogplugin.CookieConfigFromBaseURL("https://cog.example.com")
	if !c.Secure {
		t.Fatal("https BaseURL should set Secure=true")
	}
}

func TestCookieConfig_NotSecureFromHTTP(t *testing.T) {
	c := cogplugin.CookieConfigFromBaseURL("http://localhost:8580")
	if c.Secure {
		t.Fatal("http BaseURL should set Secure=false")
	}
}

func TestCookieConfig_SetAppliesDefaults(t *testing.T) {
	c := cogplugin.CookieConfig{Secure: true}
	rec := httptest.NewRecorder()
	c.Set(rec, &http.Cookie{Name: "test", Value: "v"})

	got := rec.Result().Cookies()
	if len(got) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(got))
	}
	ck := got[0]
	if !ck.Secure {
		t.Fatal("Secure flag not applied")
	}
	if !ck.HttpOnly {
		t.Fatal("HttpOnly default not applied")
	}
	if ck.Path != "/" {
		t.Fatalf("Path = %q, want /", ck.Path)
	}
	if ck.SameSite != http.SameSiteLaxMode {
		t.Fatalf("SameSite = %v, want Lax", ck.SameSite)
	}
}

func TestCookieConfig_SetPreservesExplicitFields(t *testing.T) {
	c := cogplugin.CookieConfig{Secure: false}
	rec := httptest.NewRecorder()
	c.Set(rec, &http.Cookie{
		Name:     "test",
		Value:    "v",
		Path:     "/custom",
		SameSite: http.SameSiteStrictMode,
		MaxAge:   60,
	})

	got := rec.Result().Cookies()
	ck := got[0]
	if ck.Path != "/custom" {
		t.Fatalf("Path = %q, want /custom", ck.Path)
	}
	if ck.SameSite != http.SameSiteStrictMode {
		t.Fatalf("SameSite = %v, want Strict", ck.SameSite)
	}
	if ck.MaxAge != 60 {
		t.Fatalf("MaxAge = %d, want 60", ck.MaxAge)
	}
}

func TestCookieConfig_ClearWritesDeletionCookie(t *testing.T) {
	c := cogplugin.CookieConfig{Secure: true}
	rec := httptest.NewRecorder()
	c.Clear(rec, "test")

	got := rec.Result().Cookies()
	if len(got) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(got))
	}
	ck := got[0]
	if ck.Name != "test" {
		t.Fatalf("Name = %q", ck.Name)
	}
	if ck.MaxAge != -1 {
		t.Fatalf("MaxAge = %d, want -1", ck.MaxAge)
	}
	if !ck.Secure {
		t.Fatal("Clear should respect Secure config")
	}
}

// --- ValidateCSRF: header takes precedence ---

func TestValidateCSRF_HeaderBeatsForm(t *testing.T) {
	secret := []byte("s")
	good := CSRFToken("sess1", secret)

	req := httptest.NewRequest("POST", "/", strings.NewReader(""))
	req.Header.Set("X-CSRF-Token", good)
	// Form has a bad value — should be ignored because header is present.
	req.PostForm = map[string][]string{"csrf_token": {"junk"}}
	if !ValidateCSRF(req, "sess1", secret) {
		t.Fatal("header token should be accepted")
	}
}
