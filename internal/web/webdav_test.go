package web_test

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/files"
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
	env := cogtest.New(t, calendar.New())
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

// A file whose metadata outlived its bytes (removed behind Cog's back) is
// gone to the client: 404, as the federated endpoint answers, not 500.
func TestWebDAV_GETMissingBytes(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	prefix := "/remote.php/dav/files/" + alice.ID

	webdavReq(t, env, "PUT", prefix+"/hello.txt", "hello", alice.Auth).Status(201)
	require.NoError(t, os.Remove(filepath.Join(env.Providers.Plugins.UserFilesDir("alice"), "hello.txt")))

	env.Get(t, prefix+"/hello.txt", alice.Auth).Status(404)
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
	plug, _ := env.Providers.Plugins.Plugin("files")
	fp := plug.(*files.Plugin)
	meta, err := fp.GetByPath(alice.Username, "/music.mp3")
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

func TestWebDAV_COPY(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file
	webdavReq(t, env, "PUT", prefix+"/original.txt", "copy me", alice.Auth).Status(201)

	// COPY it to a new path
	dest := env.Providers.Config.BaseURL + prefix + "/copied.txt"
	webdavReq(t, env, "COPY", prefix+"/original.txt", "",
		alice.Auth, cogtest.Header("Destination", dest)).Status(201)

	// Both files should exist and have the same content
	env.Get(t, prefix+"/original.txt", alice.Auth).Status(200).Equals("copy me")
	env.Get(t, prefix+"/copied.txt", alice.Auth).Status(200).Equals("copy me")

	// PROPFIND should list both
	resp := webdavReq(t, env, "PROPFIND", prefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	body := resp.ReadBody()
	require.Contains(t, body, "original.txt")
	require.Contains(t, body, "copied.txt")
}

func TestWebDAV_LockUnlock(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file to lock
	webdavReq(t, env, "PUT", prefix+"/locked.txt", "lock me", alice.Auth).Status(201)

	// LOCK the file
	lockBody := `<?xml version="1.0" encoding="utf-8" ?>
<D:lockinfo xmlns:D="DAV:">
  <D:lockscope><D:exclusive/></D:lockscope>
  <D:locktype><D:write/></D:locktype>
</D:lockinfo>`
	resp := webdavReq(t, env, "LOCK", prefix+"/locked.txt", lockBody, alice.Auth)
	resp.Status(200)
	body := resp.ReadBody()
	require.Contains(t, body, "lockdiscovery")
	require.Contains(t, body, "opaquelocktoken:")

	// Extract Lock-Token header
	lockToken := resp.Header.Get("Lock-Token")
	require.NotEmpty(t, lockToken)

	// UNLOCK the file
	webdavReq(t, env, "UNLOCK", prefix+"/locked.txt", "",
		alice.Auth, cogtest.Header("Lock-Token", lockToken)).Status(204)
}

func TestWebDAV_Report(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file and mark it as favorite via the files plugin
	webdavReq(t, env, "PUT", prefix+"/fav.txt", "favorite content", alice.Auth).Status(201)
	plug, _ := env.Providers.Plugins.Plugin("files")
	fp := plug.(*files.Plugin)
	require.NoError(t, fp.SetFavorite(alice.Username, "/fav.txt", true))

	// PUT another file that is not a favorite
	webdavReq(t, env, "PUT", prefix+"/nofav.txt", "not a favorite", alice.Auth).Status(201)

	// Send a REPORT filter-files request for favorites
	reportBody := `<?xml version="1.0" encoding="utf-8" ?>
<oc:filter-files xmlns:D="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns">
  <oc:filter-rules>
    <oc:favorite>1</oc:favorite>
  </oc:filter-rules>
</oc:filter-files>`
	resp := webdavReq(t, env, "REPORT", prefix+"/", reportBody, alice.Auth)
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "fav.txt")
	require.NotContains(t, body, "nofav.txt")
}

func TestWebDAV_Proppatch(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	prefix := "/remote.php/dav/files/" + alice.ID

	// PUT a file
	webdavReq(t, env, "PUT", prefix+"/star.txt", "star me", alice.Auth).Status(201)

	// PROPPATCH to set favorite
	proppatchBody := `<?xml version="1.0" encoding="utf-8" ?>
<D:propertyupdate xmlns:D="DAV:" xmlns:oc="http://owncloud.org/ns">
  <D:set>
    <D:prop>
      <oc:favorite>1</oc:favorite>
    </D:prop>
  </D:set>
</D:propertyupdate>`
	resp := webdavReq(t, env, "PROPPATCH", prefix+"/star.txt", proppatchBody, alice.Auth)
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "oc:favorite")
	require.Contains(t, body, "200 OK")

	// Verify the file is now a favorite via REPORT
	reportBody := `<?xml version="1.0" encoding="utf-8" ?>
<oc:filter-files xmlns:D="DAV:" xmlns:oc="http://owncloud.org/ns">
  <oc:filter-rules>
    <oc:favorite>1</oc:favorite>
  </oc:filter-rules>
</oc:filter-files>`
	resp2 := webdavReq(t, env, "REPORT", prefix+"/", reportBody, alice.Auth)
	resp2.Status(207)
	require.Contains(t, resp2.ReadBody(), "star.txt")
}

func TestChunkedUpload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	uploadPrefix := "/remote.php/dav/uploads/" + alice.ID
	filePrefix := "/remote.php/dav/files/" + alice.ID

	// Step 1: MKCOL to create upload session
	webdavReq(t, env, "MKCOL", uploadPrefix+"/transfer123/", "", alice.Auth).Status(201)

	// Step 2: PUT chunks (sorted by name to assemble in order)
	webdavReq(t, env, "PUT", uploadPrefix+"/transfer123/00001", "hello ", alice.Auth).Status(201)
	webdavReq(t, env, "PUT", uploadPrefix+"/transfer123/00002", "world", alice.Auth).Status(201)

	// Step 3: MOVE .file to assemble at final destination
	dest := env.Providers.Config.BaseURL + filePrefix + "/assembled.txt"
	webdavReq(t, env, "MOVE", uploadPrefix+"/transfer123/.file", "",
		alice.Auth, cogtest.Header("Destination", dest)).Status(201)

	// Step 4: Verify the assembled file content
	env.Get(t, filePrefix+"/assembled.txt", alice.Auth).
		Status(200).
		Equals("hello world")
}

func TestChunkedUploadAbort(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	uploadPrefix := "/remote.php/dav/uploads/" + alice.ID

	// MKCOL to create upload session
	webdavReq(t, env, "MKCOL", uploadPrefix+"/aborted/", "", alice.Auth).Status(201)

	// PUT a partial chunk
	webdavReq(t, env, "PUT", uploadPrefix+"/aborted/00001", "partial data", alice.Auth).Status(201)

	// DELETE to cancel the upload
	webdavReq(t, env, "DELETE", uploadPrefix+"/aborted/", "", alice.Auth).Status(204)

	// Trying to PUT another chunk should fail (session gone)
	webdavReq(t, env, "PUT", uploadPrefix+"/aborted/00002", "more data", alice.Auth).Status(404)
}

func TestTrashbin(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	filePrefix := "/remote.php/dav/files/" + alice.ID
	trashPrefix := "/remote.php/dav/trashbin/" + alice.ID + "/trash"

	// Upload a file
	webdavReq(t, env, "PUT", filePrefix+"/trashme.txt", "trash content", alice.Auth).Status(201)

	// Delete the file via WebDAV (sends to trash)
	webdavReq(t, env, "DELETE", filePrefix+"/trashme.txt", "", alice.Auth).Status(204)

	// Verify file is gone from files listing
	resp := webdavReq(t, env, "PROPFIND", filePrefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	require.NotContains(t, resp.ReadBody(), "trashme.txt")

	// PROPFIND trashbin should list the trashed file
	resp2 := webdavReq(t, env, "PROPFIND", trashPrefix+"/", "",
		alice.Auth, cogtest.Header("Depth", "1"))
	resp2.Status(207)
	trashBody := resp2.ReadBody()
	require.Contains(t, trashBody, "trashme.txt")

	// Get the trash item name from the files plugin
	plug, _ := env.Providers.Plugins.Plugin("files")
	fp := plug.(*files.Plugin)
	items, err := fp.ListTrash(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 1, len(items))
	trashName := items[0].TrashName

	// Restore via MOVE from trashbin
	dest := env.Providers.Config.BaseURL + filePrefix + "/trashme.txt"
	webdavReq(t, env, "MOVE", trashPrefix+"/"+trashName, "",
		alice.Auth, cogtest.Header("Destination", dest)).Status(201)

	// File should be back
	env.Get(t, filePrefix+"/trashme.txt", alice.Auth).
		Status(200).
		Equals("trash content")

	// Now delete again and permanently delete from trash
	webdavReq(t, env, "DELETE", filePrefix+"/trashme.txt", "", alice.Auth).Status(204)
	items, err = fp.ListTrash(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 1, len(items))

	webdavReq(t, env, "DELETE", trashPrefix+"/"+items[0].TrashName, "", alice.Auth).Status(204)

	// Trash should be empty now
	items, err = fp.ListTrash(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 0, len(items))
}

func TestCommentsCRUD(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	filePrefix := "/remote.php/dav/files/" + alice.ID

	// Upload a file
	webdavReq(t, env, "PUT", filePrefix+"/commented.txt", "comment on me", alice.Auth).Status(201)

	// Get the file ID
	plug, _ := env.Providers.Plugins.Plugin("files")
	fp := plug.(*files.Plugin)
	meta, err := fp.GetByPath(alice.Username, "/commented.txt")
	require.NoError(t, err)

	commentPath := fmt.Sprintf("/remote.php/dav/comments/files/%d", meta.ID)

	// PROPFIND comments (should be empty)
	resp := webdavReq(t, env, "PROPFIND", commentPath+"/", "", alice.Auth)
	resp.Status(207)
	body := resp.ReadBody()
	require.Contains(t, body, "multistatus")

	// POST a comment (JSON format as NC Android app does)
	commentJSON := `{"actorId":"alice","actorType":"users","verb":"comment","message":"Hello from test"}`
	resp2 := webdavReq(t, env, "POST", commentPath+"/", commentJSON,
		alice.Auth, cogtest.Header("Content-Type", "application/json"))
	resp2.Status(201)

	// Verify Content-Location header contains the new comment path
	contentLoc := resp2.Header.Get("Content-Location")
	require.Contains(t, contentLoc, fmt.Sprintf("/remote.php/dav/comments/files/%d/", meta.ID))

	// PROPFIND should now list the comment
	resp3 := webdavReq(t, env, "PROPFIND", commentPath+"/", "", alice.Auth)
	resp3.Status(207)
	body3 := resp3.ReadBody()
	require.Contains(t, body3, "Hello from test")
	require.Contains(t, body3, "alice")

	// Get the comment ID from the files plugin to delete it
	comments, err := fp.ListComments(alice.Username, meta.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(comments))

	// DELETE the comment
	webdavReq(t, env, "DELETE", fmt.Sprintf("%s/%d", commentPath, comments[0].ID), "", alice.Auth).
		Status(204)

	// Deleting it again finds nothing: a 404, not a server error.
	webdavReq(t, env, "DELETE", fmt.Sprintf("%s/%d", commentPath, comments[0].ID), "", alice.Auth).
		Status(404)
	err = fp.DeleteComment(alice.Username, comments[0].ID, alice.Username)
	require.True(t, errors.Is(err, files.ErrNotFound), err)

	// PROPFIND should no longer contain the comment message
	resp4 := webdavReq(t, env, "PROPFIND", commentPath+"/", "", alice.Auth)
	resp4.Status(207)
	require.NotContains(t, resp4.ReadBody(), "Hello from test")
}
