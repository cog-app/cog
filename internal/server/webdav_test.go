package server_test

import (
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func webdavReq(t testing.TB, env *cogtest.Env, method, path string, body string, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	return env.Do(t, req, opts...)
}

func TestWebDAV_PROPFINDRoot(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	resp := webdavReq(t, env, "PROPFIND", "/remote.php/dav/files/"+alice.ID+"/", "",
		alice.Auth, cogtest.Header("Depth", "0"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "<D:collection")
	require.Contains(t, body, "D:multistatus")
	require.Contains(t, body, `xmlns:D="DAV:"`)
	require.Contains(t, body, `xmlns:oc="http://owncloud.org/ns"`)
	require.Contains(t, body, `xmlns:nc="http://nextcloud.org/ns"`)
	// Root must have a real fileid (not 0) so NC clients accept it
	require.NotContains(t, body, "<oc:fileid>0</oc:fileid>")
	require.Contains(t, body, "<oc:fileid>")
}

func TestWebDAV_PUTThenGET(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file
	webdavReq(t, env, "PUT", prefix+"/hello.txt", "hello webdav", alice.Auth).
		Status(201)

	// GET it back
	env.Get(t, prefix+"/hello.txt", alice.Auth).
		Status(200).
		Equals("hello webdav")
}

func TestWebDAV_PUTThenPROPFIND(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file
	webdavReq(t, env, "PUT", prefix+"/doc.txt", "some content", alice.Auth).
		Status(201)

	// PROPFIND parent dir (depth=1) should list the file
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "doc.txt")
	require.Contains(t, body, "text/plain")
}

func TestWebDAV_DELETE(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT then DELETE
	webdavReq(t, env, "PUT", prefix+"/gone.txt", "bye", alice.Auth).Status(201)
	webdavReq(t, env, "DELETE", prefix+"/gone.txt", "", alice.Auth).Status(204)

	// PROPFIND root should not contain the file
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	body := resp.ReadBody()
	require.NotContains(t, body, "gone.txt")
}

func TestWebDAV_MKCOL(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// Create directory
	webdavReq(t, env, "MKCOL", prefix+"/photos", "", alice.Auth).Status(201)

	// PROPFIND root should show the collection
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "photos")
	require.Contains(t, body, "<D:collection")
}

func TestWebDAV_MOVE(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file
	webdavReq(t, env, "PUT", prefix+"/old.txt", "move me", alice.Auth).Status(201)

	// MOVE it
	dest := env.Providers.Config.BaseURL + prefix + "/new.txt"
	webdavReq(t, env, "MOVE", prefix+"/old.txt", "",
		alice.Auth, cogtest.Header("Destination", dest)).Status(201)

	// GET new path succeeds
	env.Get(t, prefix+"/new.txt", alice.Auth).
		Status(200).
		Equals("move me")

	// PROPFIND should show new.txt, not old.txt
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	body := resp.ReadBody()
	require.Contains(t, body, "new.txt")
	require.NotContains(t, body, "old.txt")
}

func TestWebDAV_DirectDownload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// Upload a file via WebDAV PUT
	webdavReq(t, env, "PUT", prefix+"/music.mp3", "fake mp3 data", alice.Auth).
		Status(201)

	// PROPFIND to get the file ID from the listing
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "music.mp3")
	require.Contains(t, body, "<oc:fileid>")

	// Extract fileid from XML (find the one that's not "0")
	// The PROPFIND lists root (fileid=0) and music.mp3 (fileid=N)
	require.Contains(t, body, `xmlns:oc="http://owncloud.org/ns"`)

	// Use the direct download API with a known file ID
	// First get the file ID via the files plugin directly
	meta, err := env.Providers.Files.GetByPath(alice.Username, "/music.mp3")
	require.NoError(t, err)

	// POST to direct download endpoint to get a token URL
	var ocsResp struct {
		OCS struct {
			Meta struct{ Status string } `json:"meta"`
			Data struct{ URL string }    `json:"data"`
		} `json:"ocs"`
	}
	env.PostForm(t, "/ocs/v2.php/apps/dav/api/v1/direct",
		url.Values{"fileId": {fmt.Sprintf("%d", meta.ID)}}, alice.Auth).
		Status(200).JSON(&ocsResp)

	require.Equal(t, "ok", ocsResp.OCS.Meta.Status)
	require.Contains(t, ocsResp.OCS.Data.URL, "/remote.php/dav/direct/")

	// Fetch the token URL WITHOUT auth — should return the file content
	env.Get(t, ocsResp.OCS.Data.URL[len(env.Providers.Config.BaseURL):]).
		Status(200).
		Equals("fake mp3 data")
}
