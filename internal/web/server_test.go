package web_test

import (
	"net/url"
	"regexp"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

var grantCSRFRe = regexp.MustCompile(`name="csrf_token"\s+value="([^"]+)"`)

// TestNCLoginFlow replays the NextCloud Android app login sequence
// observed from a real device connecting to Cog.
func TestNCLoginFlow(t *testing.T) {
	env := cogtest.New(t).WithCookieJar(t)
	admin := env.CreateUser(t, "admin", "admin")

	// Step 1: GET /status.php — server discovery
	var status struct {
		Installed   bool   `json:"installed"`
		ProductName string `json:"productname"`
	}
	env.Get(t, "/status.php").Status(200).JSON(&status)
	require.True(t, status.Installed)
	require.Equal(t, "Cog", status.ProductName)

	// Step 2: HEAD /remote.php/dav — WebDAV presence check (must not redirect)
	env.Head(t, "/remote.php/dav", cogtest.NoRedirect).Status(200)

	// Step 3: POST /index.php/login/v2 — init login flow
	var initResp struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	env.Post(t, "/index.php/login/v2").Status(200).JSON(&initResp)
	require.NotEmpty(t, initResp.Poll.Token)
	require.NotEmpty(t, initResp.Login)

	// Extract the grant token from the login URL
	loginURL, _ := url.Parse(initResp.Login)
	grantToken := loginURL.Query().Get("token")
	require.NotEmpty(t, grantToken)

	// Step 4: GET /grant without auth → bounce to /login (303). Follow.
	env.Get(t, "/index.php/login/v2/grant?token="+grantToken).
		Status(200).
		Contains("Log in")

	// Step 5: POST /index.php/login/v2/poll — poll before grant (should be 404)
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {initResp.Poll.Token}}).
		Status(404)

	// Step 6a: log in via /login (sets session cookie in the jar).
	env.PostForm(t, "/login", url.Values{
		"username": {"admin"},
		"password": {"admin"},
	}).Status(200) // /login redirects to / which 200s after auth.

	// Step 6b: GET /grant authenticated — render confirm form, extract CSRF.
	grantPage := env.Get(t, "/index.php/login/v2/grant?token="+grantToken).
		Status(200).
		Contains("Grant access")
	m := grantCSRFRe.FindStringSubmatch(grantPage.ReadBody())
	require.True(t, len(m) == 2)
	csrf := m[1]

	// Step 6c: POST /grant with CSRF — authoritative grant.
	env.PostForm(t, "/index.php/login/v2/grant?token="+grantToken, url.Values{
		"csrf_token": {csrf},
	}).Status(200).Contains("Access granted")

	// Step 7: POST /index.php/login/v2/poll — poll after grant
	var pollResp struct {
		Server      string `json:"server"`
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {initResp.Poll.Token}}).
		Status(200).JSON(&pollResp)
	require.Equal(t, "admin", pollResp.LoginName)
	require.NotEmpty(t, pollResp.AppPassword)

	appAuth := cogtest.BasicAuth("admin", pollResp.AppPassword)

	// Step 8: GET /ocs/v2.php/cloud/user — authenticate with app password
	var ocsResp struct {
		OCS struct {
			Data struct {
				ID          string `json:"id"`
				DisplayName string `json:"display-name"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/cloud/user?format=json", appAuth).
		Status(200).JSON(&ocsResp)
	require.Equal(t, "admin", ocsResp.OCS.Data.ID)
	require.Equal(t, "admin", ocsResp.OCS.Data.DisplayName)

	// Step 9: HEAD /remote.php/dav/files/{user_id}/ — file root presence
	env.Head(t, "/remote.php/dav/files/"+admin.ID+"/", appAuth).Status(200)

	// Step 10: GET /ocs/v2.php/cloud/capabilities
	env.Get(t, "/ocs/v2.php/cloud/capabilities?format=json", appAuth).Status(200)

	// Step 11: Stub endpoints should all return 200
	stubs := []string{
		"/index.php/avatar/" + admin.ID + "/512",
		"/ocs/v2.php/apps/notifications/api/v2/notifications?format=json",
		"/ocs/v2.php/apps/recommendations/api/v1/recommendations?format=json",
		"/ocs/v2.php/apps/user_status/api/v1/user_status?format=json",
	}
	for _, path := range stubs {
		env.Get(t, path, appAuth).Status(200)
	}

	// Step 12: GET /index.php/204 — connectivity check
	env.Get(t, "/index.php/204").Status(204)
}

// TestNCLoginFlowUnauthGrant ensures POST /grant without a session cannot
// mint an app password. The grant is bound to the browser session, not to
// form-supplied credentials, so without a session there's no user to grant to.
func TestNCLoginFlowUnauthGrant(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "admin")

	var initResp struct {
		Poll  struct{ Token string } `json:"poll"`
		Login string                 `json:"login"`
	}
	env.Post(t, "/index.php/login/v2").Status(200).JSON(&initResp)

	loginURL, _ := url.Parse(initResp.Login)
	grantToken := loginURL.Query().Get("token")

	// POST grant without a session — bounces to /login, no grant happens.
	env.PostForm(t, "/index.php/login/v2/grant?token="+grantToken, url.Values{
		"csrf_token": {"anything"},
	}).Status(200).Contains("Log in")

	// Poll should still be pending — no credentials issued.
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {initResp.Poll.Token}}).
		Status(404)
}

func TestOCSSearchProviders(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	var ocsResp struct {
		OCS struct {
			Meta struct{ Status string } `json:"meta"`
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/search/providers?format=json", alice.Auth).
		Status(200).JSON(&ocsResp)

	require.Equal(t, "ok", ocsResp.OCS.Meta.Status)
	require.Equal(t, 1, len(ocsResp.OCS.Data))
	require.Equal(t, "files", ocsResp.OCS.Data[0].ID)
}

func TestOCSProfile(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	var ocsResp struct {
		OCS struct {
			Meta struct{ Status string } `json:"meta"`
			Data struct {
				UserID      string `json:"userId"`
				DisplayName string `json:"displayName"`
			} `json:"data"`
		} `json:"ocs"`
	}
	env.Get(t, "/ocs/v2.php/apps/dav/api/v1/profiles/alice?format=json", alice.Auth).
		Status(200).JSON(&ocsResp)

	require.Equal(t, "ok", ocsResp.OCS.Meta.Status)
	require.Equal(t, "alice", ocsResp.OCS.Data.UserID)
	require.Equal(t, "alice", ocsResp.OCS.Data.DisplayName)
}
