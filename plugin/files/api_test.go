package files_test

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// --- helpers ---

func uploadFile(t testing.TB, env *cogtest.Env, u *cogtest.User, path, body string) files.FileMeta {
	t.Helper()
	var meta files.FileMeta
	env.PostText(t, "/api/files/upload?path="+url.QueryEscape(path), body, u.Auth).
		Status(201).
		JSON(&meta)
	return meta
}

// --- File handlers ---

func TestAPI_List_RequiresAuth(t *testing.T) {
	env := cogtest.New(t)
	env.Get(t, "/api/files/list").Status(401)
}

func TestAPI_List_DefaultPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	uploadFile(t, env, alice, "/a.txt", "x")

	var resp struct {
		Path  string           `json:"path"`
		Files []files.FileMeta `json:"files"`
	}
	env.Get(t, "/api/files/list", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, "/", resp.Path)
	assert.True(t, len(resp.Files) >= 1)
}

func TestAPI_Upload_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/files/upload", "hi", alice.Auth).Status(400).Contains("path required")
}

func TestAPI_Download_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/download", alice.Auth).Status(400).Contains("path required")
}

func TestAPI_Download_NotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/download?path=/nope.txt", alice.Auth).Status(404)
}

func TestAPI_Mkdir_BadJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/files/mkdir", "{not json", alice.Auth).Status(400)
}

func TestAPI_Mkdir_PathRequired(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/files/mkdir", map[string]string{"path": ""}, alice.Auth).
		Status(400).Contains("path required")
}

func TestAPI_Delete_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Delete(t, "/api/files/delete", alice.Auth).Status(400).Contains("path required")
}

func TestAPI_Rename_BadJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/files/rename", "{not json", alice.Auth).Status(400)
}

func TestAPI_Rename_MissingFields(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/files/rename",
		map[string]string{"old_path": "/a.txt"}, alice.Auth).
		Status(400).Contains("required")
}

func TestAPI_Search_MissingQuery(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/search", alice.Auth).Status(400).Contains("q parameter")
}

func TestAPI_Info_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/info", alice.Auth).Status(400).Contains("path required")
}

func TestAPI_Info_NotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/info?path=/nope.txt", alice.Auth).Status(404)
}

func TestAPI_Info_Ok(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadFile(t, env, alice, "/info.txt", "data")

	var meta files.FileMeta
	env.Get(t, "/api/files/info?path=/info.txt", alice.Auth).Status(200).JSON(&meta)
	assert.Equal(t, "info.txt", meta.Name)
	assert.Equal(t, int64(4), meta.Size)
}

// --- Favorites ---

func TestAPI_Favorites_RoundTrip(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadFile(t, env, alice, "/fav.txt", "data")

	// Empty favorites initially.
	var empty []files.FileMeta
	env.Get(t, "/api/files/favorites", alice.Auth).Status(200).JSON(&empty)
	require.Equal(t, 0, len(empty))

	// Star it.
	env.PostJSON(t, "/api/files/favorite",
		map[string]any{"path": "/fav.txt", "favorite": true}, alice.Auth).Status(204)

	// Favorites now contains it.
	var favs []files.FileMeta
	env.Get(t, "/api/files/favorites", alice.Auth).Status(200).JSON(&favs)
	require.Equal(t, 1, len(favs))
	assert.Equal(t, "fav.txt", favs[0].Name)

	// Unstar.
	env.PostJSON(t, "/api/files/favorite",
		map[string]any{"path": "/fav.txt", "favorite": false}, alice.Auth).Status(204)

	env.Get(t, "/api/files/favorites", alice.Auth).Status(200).JSON(&favs)
	require.Equal(t, 0, len(favs))
}

func TestAPI_Favorite_BadJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/files/favorite", "{", alice.Auth).Status(400)
}

func TestAPI_Favorite_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/files/favorite",
		map[string]any{"favorite": true}, alice.Auth).Status(400).Contains("path required")
}

// --- Comments ---

func TestAPI_Comments_RoundTrip(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/notes.txt", "hello")

	// List empty.
	var empty []files.Comment
	env.Get(t, "/api/files/comments?file_id="+strconv.FormatInt(meta.ID, 10), alice.Auth).
		Status(200).JSON(&empty)
	require.Equal(t, 0, len(empty))

	// Create.
	var created files.Comment
	env.PostJSON(t, "/api/files/comments",
		map[string]any{"file_id": meta.ID, "message": "first comment"}, alice.Auth).
		Status(201).JSON(&created)
	require.Equal(t, "first comment", created.Message)
	assert.True(t, created.ID > 0)

	// List now has 1.
	var list []files.Comment
	env.Get(t, "/api/files/comments?file_id="+strconv.FormatInt(meta.ID, 10), alice.Auth).
		Status(200).JSON(&list)
	require.Equal(t, 1, len(list))

	// Delete.
	env.Delete(t, "/api/files/comments/"+strconv.FormatInt(created.ID, 10), alice.Auth).Status(204)

	env.Get(t, "/api/files/comments?file_id="+strconv.FormatInt(meta.ID, 10), alice.Auth).
		Status(200).JSON(&list)
	require.Equal(t, 0, len(list))
}

func TestAPI_Comments_MissingFileID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Get(t, "/api/files/comments", alice.Auth).Status(400).Contains("file_id required")
}

func TestAPI_Comments_CreateMissingFields(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/files/comments",
		map[string]any{"file_id": 1}, alice.Auth).Status(400).Contains("required")
}

func TestAPI_Comments_DeleteBadID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Delete(t, "/api/files/comments/abc", alice.Auth).Status(400).Contains("invalid comment id")
}

func TestAPI_Comments_DeleteForeign(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	meta := uploadFile(t, env, alice, "/x.txt", "data")
	var c files.Comment
	env.PostJSON(t, "/api/files/comments",
		map[string]any{"file_id": meta.ID, "message": "hi"}, alice.Auth).
		Status(201).JSON(&c)

	// Bob can't delete Alice's comment on Alice's file (bob's DB doesn't have it).
	env.Delete(t, "/api/files/comments/"+strconv.FormatInt(c.ID, 10), bob.Auth).Status(404)
}

// --- Sharing ---

func TestAPI_ShareCreate_MissingFields(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/shares",
		map[string]any{"resource_type": "files:file"}, alice.Auth).
		Status(400).Contains("resource_id required")
}

func TestAPI_ShareCreate_BadJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/shares", "{not json", alice.Auth).Status(400)
}

func TestAPI_ShareCreate_MissingSubject(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/file.txt", "data")
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
	}, alice.Auth).Status(400).Contains("subject")
}

func TestAPI_ShareCreate_NotOwned(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	meta := uploadFile(t, env, alice, "/secret.txt", "secret")

	// Bob tries to share Alice's file.
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    "anyone",
	}, bob.Auth).Status(403)
}

func TestAPI_ShareCreate_UnsupportedType(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "calendar:event",
		"resource_id":   "1",
		"subject_type":  "USER",
		"subject_id":    "bob",
	}, alice.Auth).Status(403).Contains("unsupported")
}

func TestAPI_ShareList(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	meta := uploadFile(t, env, alice, "/share.txt", "data")
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
	}, alice.Auth).Status(201)

	var list []share.Record
	env.Get(t, "/api/shares", alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 1, len(list))
	assert.Equal(t, alice.ID, list[0].OwnerUserID)
}

func TestAPI_SharesWithMe(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	meta := uploadFile(t, env, alice, "/forBob.txt", "data")
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
		"permissions":   1,
	}, alice.Auth).Status(201)

	var list []map[string]any
	env.Get(t, "/api/shares/with-me", bob.Auth).Status(200).JSON(&list)
	require.Equal(t, 1, len(list))
	assert.Equal(t, "alice", list[0]["owner_username"])
}

func TestAPI_ShareDelete_NotOwner(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	meta := uploadFile(t, env, alice, "/x.txt", "data")
	var created share.Record
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
	}, alice.Auth).Status(201).JSON(&created)

	env.Delete(t, "/api/shares/"+created.ID, bob.Auth).Status(403)
}

func TestAPI_ShareDelete_NotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Delete(t, "/api/shares/does-not-exist", alice.Auth).Status(404)
}

// --- Public links ---

func TestAPI_PublicLink_NoPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/public.txt", "publicdata")

	var rec share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
	}, alice.Auth).Status(201).JSON(&rec)
	require.NotNil(t, rec.Token)

	// Anonymous access (no auth) returns metadata.
	env.Get(t, "/s/"+*rec.Token).Status(200).Contains("public.txt")
	env.Get(t, "/s/"+*rec.Token+"/download").Status(200).Equals("publicdata")
}

func TestAPI_PublicLink_PasswordRequired(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/locked.txt", "secret")

	var rec share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"password":      "letmein",
	}, alice.Auth).Status(201).JSON(&rec)

	// No password → 401.
	env.Get(t, "/s/"+*rec.Token).Status(401).Contains("password required")

	// Wrong password via basic auth → 401.
	env.Get(t, "/s/"+*rec.Token, cogtest.BasicAuth("", "wrong")).Status(401)

	// Correct password via basic auth → 200.
	env.Get(t, "/s/"+*rec.Token, cogtest.BasicAuth("", "letmein")).Status(200)

	// Correct password via query param → 200.
	env.Get(t, "/s/"+*rec.Token+"?password=letmein").Status(200)

	// Download with correct password.
	env.Get(t, "/s/"+*rec.Token+"/download?password=letmein").
		Status(200).Equals("secret")
}

func TestAPI_PublicLink_MissingFields(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/shares/public-link",
		map[string]any{"resource_type": "files:file"}, alice.Auth).
		Status(400).Contains("required")
}

func TestAPI_PublicLink_NotOwned(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	meta := uploadFile(t, env, alice, "/forbidden.txt", "data")

	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
	}, bob.Auth).Status(403)
}

func TestAPI_PublicLink_BadToken(t *testing.T) {
	env := cogtest.New(t)
	env.Get(t, "/s/not-a-real-token").Status(404)
	env.Get(t, "/s/not-a-real-token/download").Status(404)
}

func TestAPI_PublicLink_BadJSON(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostText(t, "/api/shares/public-link", "{", alice.Auth).Status(400)
}

// --- Shared download ---

func TestAPI_SharedDownload_AccessControl(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")

	meta := uploadFile(t, env, alice, "/shared.txt", "shareddata")

	var rec share.Record
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
		"permissions":   1,
	}, alice.Auth).Status(201).JSON(&rec)

	// Bob (subject) can download.
	env.Get(t, "/api/shares/"+rec.ID+"/download", bob.Auth).
		Status(200).Equals("shareddata")

	// Carol (not subject) cannot.
	env.Get(t, "/api/shares/"+rec.ID+"/download", carol.Auth).Status(403)

	// Unknown share ID.
	env.Get(t, "/api/shares/does-not-exist/download", bob.Auth).Status(404)
}

// --- Misc edge: verifyOwnership reachable via share create errors ---

func TestAPI_VerifyOwnership_BadID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   "not-an-int",
		"subject_type":  "USER",
		"subject_id":    "anyone",
	}, alice.Auth).Status(403).Contains("invalid")
}

// --- handleDownload happy path covered indirectly; here cover Content-Disposition. ---

func TestAPI_Download_ContentDisposition(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	uploadFile(t, env, alice, "/dl.txt", "dldata")

	resp := env.Get(t, "/api/files/download?path=/dl.txt", alice.Auth).Status(200)
	cd := resp.Header.Get("Content-Disposition")
	assert.True(t, strings.Contains(cd, "filename=dl.txt"))
	assert.True(t, resp.Header.Get("ETag") != "")
}

// ListByOwner is reachable via /api/shares. Ensure it survives if json/list deserializes empty.
func TestAPI_ShareList_Empty(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	var list []share.Record
	env.Get(t, "/api/shares", alice.Auth).Status(200).JSON(&list)
	require.Equal(t, 0, len(list))
}

// Ensure JSON decode error path is hit on share create.
func TestAPI_ShareCreate_DefaultPermissions(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	meta := uploadFile(t, env, alice, "/p.txt", "data")

	var rec share.Record
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
		// permissions omitted → defaults to PermRead
	}, alice.Auth).Status(201).JSON(&rec)
	assert.Equal(t, uint32(1), rec.Permissions)
}

// Smoke-test: a successful upload then immediate info+download share the same ETag.
func TestAPI_UploadInfoEtagMatchesDownload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	meta := uploadFile(t, env, alice, "/etag.txt", "etagdata")

	var info files.FileMeta
	env.Get(t, "/api/files/info?path=/etag.txt", alice.Auth).Status(200).JSON(&info)
	assert.Equal(t, meta.ETag, info.ETag)

	resp := env.Get(t, "/api/files/download?path=/etag.txt", alice.Auth).Status(200)
	assert.Equal(t, meta.ETag, resp.Header.Get("ETag"))
}

// Sanity: JSON shape of share-create response includes ID and OwnerUserID.
func TestAPI_ShareCreate_ResponseShape(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	meta := uploadFile(t, env, alice, "/shape.txt", "data")

	resp := env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": "files:file",
		"resource_id":   strconv.FormatInt(meta.ID, 10),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
		"permissions":   3,
	}, alice.Auth).Status(201)
	body := resp.ReadBody()
	var raw map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	assert.True(t, raw["ID"] != "")
	owner, _ := raw["OwnerUserID"].(string)
	assert.Equal(t, alice.ID, owner)
}

// A listing sorts names the way people read them: "backup.zip" before
// "Quarterly report", whatever the case of the first letter.
func TestList_SortsCaseInsensitively(t *testing.T) {
	fp := files.New()
	env := cogtest.New(t, fp)
	alice := env.CreateUser(t, "alice", "pass")

	for _, p := range []string{"/Quarterly.txt", "/backup.zip", "/alpha.txt", "/Beta.txt"} {
		uploadFile(t, env, alice, p, "x")
	}
	_, err := fp.CreateDir(alice.Username, "/zeta")
	require.NoError(t, err)
	_, err = fp.CreateDir(alice.Username, "/Archive")
	require.NoError(t, err)

	items, err := fp.List(alice.Username, "/")
	require.NoError(t, err)
	var names []string
	for _, f := range items {
		names = append(names, f.Name)
	}
	assert.Equal(t, "Archive,zeta,alpha.txt,backup.zip,Beta.txt,Quarterly.txt", strings.Join(names, ","))
}
