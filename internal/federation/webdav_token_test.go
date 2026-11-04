package federation_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// --- Auth gating ---

func TestTokenWebDAV_NoAuth(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/public.php/webdav/anything")
	resp.Status(http.StatusUnauthorized)
	require.True(t, strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic"))
}

func TestTokenWebDAV_UnknownToken(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	env.CreateUser(t, "alice", "pass")

	env.Get(t, "/public.php/webdav/anything",
		cogtest.BasicAuth("", "not-a-real-token")).
		Status(http.StatusUnauthorized)
}

// --- Public-link fallback (token belongs to a share record, not a fed share) ---

func TestTokenWebDAV_PublicLink_DownloadsFile(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	meta := putFileViaAPI(t, env, alice, "/public.txt", "publicdata")

	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), 1, nil,
	)
	require.NoError(t, err)
	require.NotNil(t, rec.Token)

	// PROPFIND on the resource root returns 207 with the file's size.
	env.Do(t, mkReq(t, env, "PROPFIND", "/public.php/webdav/"),
		cogtest.BasicAuth("", *rec.Token)).
		Status(207).Contains("<d:getcontentlength>10</d:getcontentlength>")

	// GET serves bytes.
	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth("", *rec.Token)).
		Status(200).Equals("publicdata")
}

// --- Outbound federated share (the production path) ---

func TestTokenWebDAV_Outbound_Propfind(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	meta := putFileViaAPI(t, env, alice, "/shared.txt", "shared")

	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), 1, nil,
	)
	require.NoError(t, err)

	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "accepted",
		ShareID:      &rec.ID,
		RemoteHost:   "remote.example.com",
		RemoteUser:   "bob",
		LocalUserID:  alice.ID,
		Token:        *rec.Token,
		ResourceName: "shared.txt",
		ResourceType: "file",
		Permissions:  1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	// PROPFIND.
	env.Do(t, mkReq(t, env, "PROPFIND", "/public.php/webdav/"),
		cogtest.BasicAuth("", *rec.Token)).
		Status(207).Contains("<d:getcontentlength>6</d:getcontentlength>")
}

func TestTokenWebDAV_Outbound_PutGoesThroughFilesPlugin(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	// Use a directory as the shared root so PUT to a sub-path lands inside.
	mkdirViaAPI(t, env, alice, "/SharedDir")
	dirMeta := infoViaAPI(t, env, alice, "/SharedDir")

	// Read+write permissions = 3.
	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:directory", strconv.FormatInt(dirMeta.ID, 10), 3, nil,
	)
	require.NoError(t, err)

	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "accepted",
		ShareID:      &rec.ID,
		RemoteHost:   "remote.example.com",
		RemoteUser:   "bob",
		LocalUserID:  alice.ID,
		Token:        *rec.Token,
		ResourceName: "SharedDir",
		ResourceType: "directory",
		Permissions:  3,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	// PUT a file under the share root.
	req := mkReq(t, env, "PUT", "/public.php/webdav/upload.txt", "uploaded-via-fed")
	env.Do(t, req, cogtest.BasicAuth("", *rec.Token)).Status(http.StatusCreated)

	// The file is visible in Alice's namespace through the regular API —
	// proving the write went through the files plugin (metadata + quota).
	got := env.Get(t, "/api/files/download?path=/SharedDir/upload.txt", alice.Auth).
		Status(200).ReadBody()
	require.Equal(t, "uploaded-via-fed", got)
}

func TestTokenWebDAV_Outbound_ReadOnlyRejectsWrite(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	mkdirViaAPI(t, env, alice, "/ROnly")
	dirMeta := infoViaAPI(t, env, alice, "/ROnly")

	// Read-only (permissions = 1).
	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:directory", strconv.FormatInt(dirMeta.ID, 10), 1, nil,
	)
	require.NoError(t, err)

	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "accepted",
		ShareID:      &rec.ID,
		LocalUserID:  alice.ID,
		Token:        *rec.Token,
		ResourceName: "ROnly",
		ResourceType: "directory",
		Permissions:  1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	// PUT must be refused.
	req := mkReq(t, env, "PUT", "/public.php/webdav/sneaky.txt", "nope")
	env.Do(t, req, cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusForbidden)

	// MKCOL too.
	req = mkReq(t, env, "MKCOL", "/public.php/webdav/sub")
	env.Do(t, req, cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusForbidden)

	// DELETE too.
	req = mkReq(t, env, "DELETE", "/public.php/webdav/anything")
	env.Do(t, req, cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusForbidden)
}

func TestTokenWebDAV_Outbound_DeleteThroughFilesPlugin(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	mkdirViaAPI(t, env, alice, "/Bin")
	putFileViaAPI(t, env, alice, "/Bin/disposable.txt", "data")
	dirMeta := infoViaAPI(t, env, alice, "/Bin")

	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:directory", strconv.FormatInt(dirMeta.ID, 10), 3, nil,
	)
	require.NoError(t, err)

	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "accepted",
		ShareID:      &rec.ID,
		LocalUserID:  alice.ID,
		Token:        *rec.Token,
		ResourceName: "Bin",
		ResourceType: "directory",
		Permissions:  3,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	req := mkReq(t, env, "DELETE", "/public.php/webdav/disposable.txt")
	env.Do(t, req, cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusNoContent)

	// File metadata gone from owner namespace.
	env.Get(t, "/api/files/info?path=/Bin/disposable.txt", alice.Auth).
		Status(http.StatusNotFound)
}

// TestTokenWebDAV_OwnerMountElsewhere shares a folder whose owner's files
// live on a volume outside the data directory. The handler must find them
// through the owner's mounts, as the owner's own WebDAV does, rather than by
// assuming a layout under the data directory.
func TestTokenWebDAV_OwnerMountElsewhere(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	volDir := t.TempDir()
	vol, err := env.Providers.Volumes.Create("elsewhere", "", "file://"+volDir, nil, "{}")
	require.NoError(t, err)
	_, err = env.Providers.Mounts.Create(alice.ID, "/", vol.ID, "alice", false)
	require.NoError(t, err)

	mkdirViaAPI(t, env, alice, "/Docs")
	mkdirViaAPI(t, env, alice, "/Docs/Sub")
	putFileViaAPI(t, env, alice, "/Docs/readme.txt", "hello from a mount")
	putFileViaAPI(t, env, alice, "/secret.txt", "not shared")
	dirMeta := infoViaAPI(t, env, alice, "/Docs")

	// The bytes are on the volume, not under the data directory.
	onVolume, err := os.ReadFile(filepath.Join(volDir, "alice", "Docs", "readme.txt"))
	require.NoError(t, err)
	require.Equal(t, "hello from a mount", string(onVolume))

	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:directory", strconv.FormatInt(dirMeta.ID, 10), 1, nil,
	)
	require.NoError(t, err)
	fs := &federation.FederatedShare{
		Direction:    "outbound",
		State:        "accepted",
		ShareID:      &rec.ID,
		RemoteHost:   "remote.example.com",
		RemoteUser:   "bob",
		LocalUserID:  alice.ID,
		Token:        *rec.Token,
		ResourceName: "Docs",
		ResourceType: "directory",
		Permissions:  1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))
	tok := cogtest.BasicAuth("", *rec.Token)

	// The listing shows the folder and both children.
	env.Do(t, mkReq(t, env, "PROPFIND", "/public.php/webdav/"), tok).
		Status(207).
		Contains("<d:href>/public.php/webdav/</d:href>").
		Contains("<d:href>/public.php/webdav/readme.txt</d:href>").
		Contains("<d:getcontentlength>18</d:getcontentlength>").
		Contains("<d:href>/public.php/webdav/Sub/</d:href>")

	// Depth 0 is the folder alone.
	req := mkReq(t, env, "PROPFIND", "/public.php/webdav/")
	req.Header.Set("Depth", "0")
	env.Do(t, req, tok).Status(207).NotContains("readme.txt")

	env.Get(t, "/public.php/webdav/readme.txt", tok).
		Status(200).Equals("hello from a mount")
	env.Get(t, "/public.php/webdav/missing.txt", tok).Status(http.StatusNotFound)

	// Climbing out of the share stays inside it. Through the server mux a
	// ".." never arrives (the mux redirects to the cleaned path), so the
	// handler is called directly with the raw path: it must resolve to the
	// share's own secret.txt, which doesn't exist, not alice's /secret.txt.
	fp, ok := env.Providers.Plugins.Plugin("files")
	require.True(t, ok)
	h := &federation.TokenWebDAVHandler{
		Log:    env.Providers.Log,
		Store:  env.Providers.Federation,
		Shares: env.Providers.Shares,
		Users:  env.Providers.Users,
		Files:  fp.(*files.Plugin),
		Policy: env.Providers.FedPolicy,
	}
	for _, p := range []string{"/public.php/webdav/../secret.txt", "/public.php/webdav/Sub/../../secret.txt"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.SetBasicAuth("", *rec.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.False(t, strings.Contains(w.Body.String(), "not shared"))
	}
}

// --- Direction enforcement: inbound shares must not be accessible from this endpoint ---

func TestTokenWebDAV_InboundShareRejected(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	// An inbound share is something a remote sent us; the local token is the
	// peer's secret. Local clients should not be able to read out the local
	// filesystem using it.
	token := "inbound-token-from-remote"
	fs := &federation.FederatedShare{
		Direction:    "inbound",
		State:        "accepted",
		RemoteHost:   "remote.example.com",
		RemoteUser:   "bob",
		LocalUserID:  alice.ID,
		Token:        token,
		ResourceName: "remote.txt",
		ResourceType: "file",
		Permissions:  1,
	}
	require.NoError(t, env.Providers.Federation.Create(fs))

	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth("", token)).
		Status(http.StatusForbidden)
}

// --- OPTIONS preflight ---

func TestTokenWebDAV_Options(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")

	meta := putFileViaAPI(t, env, alice, "/o.txt", "x")
	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), 1, nil,
	)
	require.NoError(t, err)

	resp := env.Do(t, mkReq(t, env, "OPTIONS", "/public.php/webdav/"),
		cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusOK)
	require.True(t, strings.Contains(resp.Header.Get("Allow"), "PUT"))
	require.True(t, strings.Contains(resp.Header.Get("DAV"), "1"))
}

// --- helpers ---

func putFileViaAPI(t testing.TB, env *cogtest.Env, u *cogtest.User, path, body string) struct {
	ID int64 `json:"ID"`
} {
	t.Helper()
	var meta struct {
		ID int64 `json:"ID"`
	}
	env.PostText(t, "/api/files/upload?path="+url.QueryEscape(path), body, u.Auth).
		Status(201).JSON(&meta)
	return meta
}

func mkdirViaAPI(t testing.TB, env *cogtest.Env, u *cogtest.User, path string) {
	t.Helper()
	env.PostJSON(t, "/api/files/mkdir",
		map[string]string{"path": path}, u.Auth).Status(201)
}

func infoViaAPI(t testing.TB, env *cogtest.Env, u *cogtest.User, path string) struct {
	ID int64 `json:"ID"`
} {
	t.Helper()
	var meta struct {
		ID int64 `json:"ID"`
	}
	env.Get(t, "/api/files/info?path="+url.QueryEscape(path), u.Auth).
		Status(200).JSON(&meta)
	return meta
}

func mkReq(t testing.TB, env *cogtest.Env, method, path string, body ...string) *http.Request {
	t.Helper()
	var b *strings.Reader
	if len(body) > 0 {
		b = strings.NewReader(body[0])
	}
	if b == nil {
		return cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, nil)
	}
	return cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, b)
}

// TestTokenWebDAV_Outbound_RefusedWhenFederationOff: a server this one no
// longer federates with loses access to what was shared with it.
func TestTokenWebDAV_Outbound_RefusedWhenFederationOff(t *testing.T) {
	env := cogtest.New(t)
	enableFederation(t, env)
	alice := env.CreateUser(t, "alice", "pass")
	putFileViaAPI(t, env, alice, "/note.txt", "hi")
	meta := infoViaAPI(t, env, alice, "/note.txt")

	rec, err := env.Providers.Shares.CreatePublicLink(alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), 1, nil)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Federation.Create(&federation.FederatedShare{
		Direction: "outbound", State: "accepted", ShareID: &rec.ID,
		RemoteHost: "remote.example.com", RemoteUser: "bob", LocalUserID: alice.ID,
		Token: *rec.Token, ResourceName: "note.txt", ResourceType: "file", Permissions: 1,
	}))
	tok := cogtest.BasicAuth("", *rec.Token)
	env.Get(t, "/public.php/webdav/", tok).Status(200).Equals("hi")

	require.NoError(t, env.Providers.Settings.SetBool(settings.KeyFederationEnabled, false))
	env.Get(t, "/public.php/webdav/", tok).Status(http.StatusForbidden)
}

// A public link with a password asks for it here too, as Nextcloud does: the
// token is the username and the link's password the password.
func TestTokenWebDAV_PublicLink_PasswordRequired(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := putFileViaAPI(t, env, alice, "/secret.txt", "secret")

	rec, err := env.Providers.Shares.CreatePublicLinkWithPassword(
		alice.ID, "files:file", strconv.FormatInt(meta.ID, 10), 1, nil, "letmein",
	)
	require.NoError(t, err)

	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth("", *rec.Token)).Status(http.StatusUnauthorized)
	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth(*rec.Token, "wrong")).Status(http.StatusUnauthorized)
	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth(*rec.Token, "letmein")).Status(200).Equals("secret")
}

// Another kind of public link — a calendar's feed, whose URL is published —
// is not a file share, though its collection ID may equal a file's ID.
func TestTokenWebDAV_PublicLink_OtherTypeRefused(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := putFileViaAPI(t, env, alice, "/private.txt", "private")

	rec, err := env.Providers.Shares.CreatePublicLink(
		alice.ID, "dav:collection", strconv.FormatInt(meta.ID, 10), 1, nil,
	)
	require.NoError(t, err)
	env.Get(t, "/public.php/webdav/", cogtest.BasicAuth("", *rec.Token)).
		Status(http.StatusUnauthorized).NotContains("private")
}
