package web_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/bodylimit"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
)

// The protections every route gets without asking (audit pattern 3).

// TestCrossSiteMutationRefused: a page on another site, even a sibling
// subdomain, cannot make the browser post with the credentials it holds,
// on a route with no CSRF token check of its own.
func TestCrossSiteMutationRefused(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	for _, site := range []string{"cross-site", "same-site"} {
		env.Put(t, "/remote.php/dav/files/alice/x.txt", strings.NewReader("pwned"),
			cogtest.BasicAuth("alice", "pass"), cogtest.Header("Sec-Fetch-Site", site)).Status(403)
	}
	env.Put(t, "/remote.php/dav/files/alice/x.txt", strings.NewReader("ok"),
		cogtest.BasicAuth("alice", "pass"), cogtest.Header("Sec-Fetch-Site", "same-origin")).Status(201)
	// A sync client sends neither header.
	env.Put(t, "/remote.php/dav/files/alice/y.txt", strings.NewReader("ok"),
		cogtest.BasicAuth("alice", "pass")).Status(201)
}

// TestAuthenticatedResponsesArePrivate: no shared cache keeps what a
// signed-in user was sent.
func TestAuthenticatedResponsesArePrivate(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	resp := env.Get(t, "/ocs/v2.php/cloud/user?format=json", alice.Auth, cogtest.Header("OCS-APIRequest", "true")).Status(200)
	assert.Contains(t, resp.Header.Get("Cache-Control"), "private")
}

// TestBodyLimitDefault: a route that did not ask for more refuses a body
// over the default, while uploads take more.
func TestBodyLimitDefault(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	big := strings.Repeat("x", bodylimit.Default+1)
	resp := env.Put(t, "/remote.php/dav/files/alice/big.txt", strings.NewReader(big), alice.Auth)
	assert.Equal(t, 201, resp.StatusCode)
	resp = env.PostText(t, "/api/shares", big, alice.Auth)
	assert.True(t, resp.StatusCode >= 400, "an oversized JSON body got %d", resp.StatusCode)
}

// TestAppPasswordBoundToUsername: Basic credentials pairing one user's name
// with another's app password sign in as nobody.
func TestAppPasswordBoundToUsername(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	token, _, err := env.Providers.AppPasswords.Create(bob.ID, "client")
	if err != nil {
		t.Fatal(err)
	}
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", cogtest.BasicAuth("alice", token), cogtest.Header("OCS-APIRequest", "true")).Status(401)
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", cogtest.BasicAuth("bob", token), cogtest.Header("OCS-APIRequest", "true")).Status(200)
}
