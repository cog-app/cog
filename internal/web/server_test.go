package web_test

import (
	"net/url"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestNCLoginFlow replays the NextCloud Android app login sequence
// observed from a real device connecting to Cog.
func TestNCLoginFlow(t *testing.T) {
	env := cogtest.New(t)
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

	// Step 4: GET /index.php/login/v2/grant — render login form
	env.Get(t, "/index.php/login/v2/grant?token="+grantToken).
		Status(200).
		Contains("<form")

	// Step 5: POST /index.php/login/v2/poll — poll before grant (should be 404)
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {initResp.Poll.Token}}).
		Status(404)

	// Step 6: POST /index.php/login/v2/grant — submit credentials
	env.PostForm(t, "/index.php/login/v2/grant?token="+grantToken,
		url.Values{"username": {"admin"}, "password": {"admin"}}).
		Status(200).
		Contains("Login Granted")

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

// TestNCLoginFlowBadPassword ensures wrong credentials don't grant access.
func TestNCLoginFlowBadPassword(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "admin", "admin")

	// Init flow
	var initResp struct {
		Poll  struct{ Token string } `json:"poll"`
		Login string                 `json:"login"`
	}
	env.Post(t, "/index.php/login/v2").Status(200).JSON(&initResp)

	loginURL, _ := url.Parse(initResp.Login)
	grantToken := loginURL.Query().Get("token")

	// Submit bad password
	env.PostForm(t, "/index.php/login/v2/grant?token="+grantToken,
		url.Values{"username": {"admin"}, "password": {"wrong"}}).
		Status(200).
		Contains("Invalid")

	// Poll should still be pending
	env.PostForm(t, "/index.php/login/v2/poll", url.Values{"token": {initResp.Poll.Token}}).
		Status(404)
}
