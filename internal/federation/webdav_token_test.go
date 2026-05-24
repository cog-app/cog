package federation_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/federation"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// --- Auth gating ---

func TestTokenWebDAV_NoAuth(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	resp := env.Get(t, "/public.php/webdav/anything")
	resp.Status(http.StatusUnauthorized)
	require.True(t, strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic"))
}

func TestTokenWebDAV_UnknownToken(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")

	env.Get(t, "/public.php/webdav/anything",
		cogtest.BasicAuth("", "not-a-real-token")).
		Status(http.StatusUnauthorized)
}

// --- Public-link fallback (token belongs to a share record, not a fed share) ---

func TestTokenWebDAV_PublicLink_DownloadsFile(t *testing.T) {
	env := cogtest.New(t)
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

// --- Direction enforcement: inbound shares must not be accessible from this endpoint ---

func TestTokenWebDAV_InboundShareRejected(t *testing.T) {
	env := cogtest.New(t)
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
