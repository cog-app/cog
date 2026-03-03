package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/server"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// newTestServer creates a test server with a fresh in-memory DB and a test user.
// Returns the server, the test user, and the plaintext password.
func newTestServer(t *testing.T) (*httptest.Server, *user.User) {
	t.Helper()

	sysDB, err := db.OpenSystem(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { sysDB.Close() })

	users := user.NewStore(sysDB)
	u, err := users.Create("admin", "admin")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	ts := httptest.NewServer(server.New(server.Config{
		DB:      sysDB,
		BaseURL: "PLACEHOLDER",
	}))
	t.Cleanup(ts.Close)

	// Re-create the handler with the real base URL now that we know it.
	ts.Config.Handler = server.New(server.Config{
		DB:      sysDB,
		BaseURL: ts.URL,
	})

	return ts, u
}

// TestNCLoginFlow replays the NextCloud Android app login sequence
// observed from a real device connecting to Cog.
func TestNCLoginFlow(t *testing.T) {
	ts, u := newTestServer(t)

	// Step 1: GET /status.php — server discovery
	resp := get(t, ts, "/status.php", "")
	expectStatus(t, resp, 200)
	var status map[string]any
	decodeJSON(t, resp, &status)
	if status["installed"] != true {
		t.Fatalf("status.installed = %v, want true", status["installed"])
	}
	if status["productname"] != "Cog" {
		t.Fatalf("status.productname = %v, want Cog", status["productname"])
	}

	// Step 2: HEAD /remote.php/dav — WebDAV presence check (must not redirect)
	req, _ := http.NewRequest("HEAD", ts.URL+"/remote.php/dav", nil)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		t.Fatal("HEAD /remote.php/dav must not redirect")
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HEAD /remote.php/dav: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HEAD /remote.php/dav = %d, want 200", resp.StatusCode)
	}

	// Step 3: POST /index.php/login/v2 — init login flow
	resp = post(t, ts, "/index.php/login/v2", "", nil)
	expectStatus(t, resp, 200)
	var initResp struct {
		Poll struct {
			Token    string `json:"token"`
			Endpoint string `json:"endpoint"`
		} `json:"poll"`
		Login string `json:"login"`
	}
	decodeJSON(t, resp, &initResp)
	if initResp.Poll.Token == "" {
		t.Fatal("poll token is empty")
	}
	if initResp.Login == "" {
		t.Fatal("login URL is empty")
	}

	// Extract the grant token from the login URL
	loginURL, _ := url.Parse(initResp.Login)
	grantToken := loginURL.Query().Get("token")
	if grantToken == "" {
		t.Fatal("grant token is empty")
	}

	// Step 4: GET /index.php/login/v2/grant — render login form
	resp = get(t, ts, "/index.php/login/v2/grant?token="+grantToken, "")
	expectStatus(t, resp, 200)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "<form") {
		t.Fatal("grant page should contain a form")
	}

	// Step 5: POST /index.php/login/v2/poll — poll before grant (should be 404)
	resp = post(t, ts, "/index.php/login/v2/poll", "", url.Values{"token": {initResp.Poll.Token}})
	expectStatus(t, resp, 404)

	// Step 6: POST /index.php/login/v2/grant — submit credentials
	resp = post(t, ts, "/index.php/login/v2/grant?token="+grantToken, "",
		url.Values{"username": {"admin"}, "password": {"admin"}})
	expectStatus(t, resp, 200)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Login Granted") {
		t.Fatalf("expected grant success page, got: %s", body)
	}

	// Step 7: POST /index.php/login/v2/poll — poll after grant (should return credentials)
	resp = post(t, ts, "/index.php/login/v2/poll", "", url.Values{"token": {initResp.Poll.Token}})
	expectStatus(t, resp, 200)
	var pollResp struct {
		Server      string `json:"server"`
		LoginName   string `json:"loginName"`
		AppPassword string `json:"appPassword"`
	}
	decodeJSON(t, resp, &pollResp)
	if pollResp.LoginName != "admin" {
		t.Fatalf("loginName = %q, want admin", pollResp.LoginName)
	}
	if pollResp.AppPassword == "" {
		t.Fatal("appPassword is empty")
	}
	if pollResp.Server != ts.URL {
		t.Fatalf("server = %q, want %q", pollResp.Server, ts.URL)
	}

	// Step 8: GET /ocs/v2.php/cloud/user — authenticate with app password
	resp = get(t, ts, "/ocs/v2.php/cloud/user?format=json", basicAuth("admin", pollResp.AppPassword))
	expectStatus(t, resp, 200)
	var ocsResp struct {
		OCS struct {
			Data struct {
				ID          string `json:"id"`
				DisplayName string `json:"display-name"`
			} `json:"data"`
		} `json:"ocs"`
	}
	decodeJSON(t, resp, &ocsResp)
	if ocsResp.OCS.Data.ID != u.ID {
		t.Fatalf("user id = %q, want %q", ocsResp.OCS.Data.ID, u.ID)
	}
	if ocsResp.OCS.Data.DisplayName != "admin" {
		t.Fatalf("display-name = %q, want admin", ocsResp.OCS.Data.DisplayName)
	}

	// Step 9: HEAD /remote.php/dav/files/{user_id}/ — file root presence
	req, _ = http.NewRequest("HEAD", ts.URL+"/remote.php/dav/files/"+u.ID+"/", nil)
	req.SetBasicAuth("admin", pollResp.AppPassword)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("HEAD dav/files: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HEAD dav/files = %d, want 200", resp.StatusCode)
	}

	// Step 10: GET /ocs/v2.php/cloud/capabilities — capabilities
	resp = get(t, ts, "/ocs/v2.php/cloud/capabilities?format=json", basicAuth("admin", pollResp.AppPassword))
	expectStatus(t, resp, 200)

	// Step 11: Stub endpoints should all return 200
	stubs := []string{
		"/index.php/avatar/" + u.ID + "/512",
		"/ocs/v2.php/apps/notifications/api/v2/notifications?format=json",
		"/ocs/v2.php/apps/recommendations/api/v1/recommendations?format=json",
		"/ocs/v2.php/apps/user_status/api/v1/user_status?format=json",
	}
	for _, path := range stubs {
		resp = get(t, ts, path, basicAuth("admin", pollResp.AppPassword))
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// Step 12: GET /index.php/204 — connectivity check
	resp = get(t, ts, "/index.php/204", "")
	if resp.StatusCode != 204 {
		t.Fatalf("GET /index.php/204 = %d, want 204", resp.StatusCode)
	}
	resp.Body.Close()
}

// TestNCLoginFlowBadPassword ensures wrong credentials don't grant access.
func TestNCLoginFlowBadPassword(t *testing.T) {
	ts, _ := newTestServer(t)

	// Init flow
	resp := post(t, ts, "/index.php/login/v2", "", nil)
	expectStatus(t, resp, 200)
	var initResp struct {
		Poll struct{ Token string } `json:"poll"`
		Login string                `json:"login"`
	}
	decodeJSON(t, resp, &initResp)

	loginURL, _ := url.Parse(initResp.Login)
	grantToken := loginURL.Query().Get("token")

	// Submit bad password
	resp = post(t, ts, "/index.php/login/v2/grant?token="+grantToken, "",
		url.Values{"username": {"admin"}, "password": {"wrong"}})
	expectStatus(t, resp, 200)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "Invalid") {
		t.Fatal("expected error message for bad password")
	}

	// Poll should still be pending
	resp = post(t, ts, "/index.php/login/v2/poll", "", url.Values{"token": {initResp.Poll.Token}})
	expectStatus(t, resp, 404)
}

// --- helpers ---

func get(t *testing.T, ts *httptest.Server, path, authHeader string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+path, nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func post(t *testing.T, ts *httptest.Server, path, authHeader string, form url.Values) *http.Response {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest("POST", ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func expectStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	if resp.StatusCode != want {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s = %d, want %d\nbody: %s", resp.Request.Method, resp.Request.URL, resp.StatusCode, want, body)
	}
}

func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode JSON from %s: %v", resp.Request.URL, err)
	}
}

func basicAuth(user, pass string) string {
	req, _ := http.NewRequest("GET", "/", nil)
	req.SetBasicAuth(user, pass)
	return req.Header.Get("Authorization")
}
