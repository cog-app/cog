package web_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/cogtest"
	"git.duckfam.us/jj-software/cog/internal/share"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

func TestFileUploadAndDownload(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload a file
	var meta files.FileMeta
	env.PostText(t, "/api/files/upload?path=/hello.txt", "hello world", alice.Auth).
		Status(201).
		JSON(&meta)
	require.Equal(t, "hello.txt", meta.Name)
	require.Equal(t, int64(11), meta.Size)
	assert.True(t, meta.ID > 0)

	// Download it
	env.Get(t, "/api/files/download?path=/hello.txt", alice.Auth).
		Status(200).
		Equals("hello world")
}

func TestFileList(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Create a directory
	env.PostJSON(t, "/api/files/mkdir", map[string]string{"path": "/docs"}, alice.Auth).Status(201)

	// Upload a file in it
	env.PostText(t, "/api/files/upload?path=/docs/readme.txt", "readme content", alice.Auth).Status(201)

	// List root
	var listResp struct {
		Path  string           `json:"path"`
		Files []files.FileMeta `json:"files"`
	}
	env.Get(t, "/api/files/list?path=/", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, "/", listResp.Path)

	// Find the docs directory in the listing
	found := false
	for _, f := range listResp.Files {
		if f.Name == "docs" && f.IsDir {
			found = true
		}
	}
	require.True(t, found)

	// List docs directory
	env.Get(t, "/api/files/list?path=/docs", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, "/docs", listResp.Path)
	found = false
	for _, f := range listResp.Files {
		if f.Name == "readme.txt" {
			found = true
			require.Equal(t, int64(14), f.Size)
		}
	}
	require.True(t, found)
}

func TestFileDelete(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload
	env.PostText(t, "/api/files/upload?path=/todelete.txt", "delete me", alice.Auth).Status(201)

	// Delete
	env.Delete(t, "/api/files/delete?path=/todelete.txt", alice.Auth).Status(204)

	// Verify gone
	env.Get(t, "/api/files/info?path=/todelete.txt", alice.Auth).Status(404)
}

func TestFileRename(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload
	var meta files.FileMeta
	env.PostText(t, "/api/files/upload?path=/old.txt", "rename me", alice.Auth).
		Status(201).
		JSON(&meta)
	origID := meta.ID

	// Rename
	var renamed files.FileMeta
	env.PostJSON(t, "/api/files/rename",
		map[string]string{"old_path": "/old.txt", "new_path": "/new.txt"}, alice.Auth).
		Status(200).JSON(&renamed)
	require.Equal(t, "new.txt", renamed.Name)
	require.Equal(t, origID, renamed.ID) // Stable ID preserved

	// Old path gone
	env.Get(t, "/api/files/info?path=/old.txt", alice.Auth).Status(404)

	// New path exists
	env.Get(t, "/api/files/info?path=/new.txt", alice.Auth).Status(200)
}

func TestShareFileWithUser(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	// Alice uploads a file
	var meta files.FileMeta
	env.PostText(t, "/api/files/upload?path=/secret.txt", "shared data", alice.Auth).
		Status(201).
		JSON(&meta)

	// Alice shares it with Bob
	var shareRec share.Record
	env.PostJSON(t, "/api/shares", map[string]any{
		"resource_type": files.ResourceType(&meta),
		"resource_id":   files.IDString(&meta),
		"subject_type":  "USER",
		"subject_id":    bob.ID,
		"permissions":   1, // read
	}, alice.Auth).Status(201).JSON(&shareRec)
	require.Equal(t, alice.ID, shareRec.OwnerUserID)
	require.Equal(t, "USER", shareRec.SubjectType)
	require.Equal(t, bob.ID, shareRec.SubjectID)

	// Bob sees it in shares-with-me
	var sharedWithMe []struct {
		ID           string `json:"ID"`
		OwnerUserID  string `json:"OwnerUserID"`
		ResourceType string `json:"ResourceType"`
	}
	env.Get(t, "/api/shares/with-me", bob.Auth).Status(200).JSON(&sharedWithMe)
	require.True(t, len(sharedWithMe) > 0)

	// Bob can download the shared file
	env.Get(t, "/api/shares/"+shareRec.ID+"/download", bob.Auth).
		Status(200).
		Equals("shared data")

	// Alice lists her shares
	var aliceShares []share.Record
	env.Get(t, "/api/shares", alice.Auth).Status(200).JSON(&aliceShares)
	require.Equal(t, 1, len(aliceShares))

	// Alice deletes the share
	env.Delete(t, "/api/shares/"+shareRec.ID, alice.Auth).Status(204)

	// Bob can no longer download
	env.Get(t, "/api/shares/"+shareRec.ID+"/download", bob.Auth).Status(404)
}

func TestPublicLink(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Alice uploads a file
	var meta files.FileMeta
	env.PostText(t, "/api/files/upload?path=/public.txt", "public content", alice.Auth).
		Status(201).
		JSON(&meta)

	// Alice creates a public link
	var linkRec share.Record
	env.PostJSON(t, "/api/shares/public-link", map[string]any{
		"resource_type": files.ResourceType(&meta),
		"resource_id":   files.IDString(&meta),
		"permissions":   1, // read
	}, alice.Auth).Status(201).JSON(&linkRec)
	require.NotNil(t, linkRec.Token)

	// Anyone can access via token (no auth)
	var linkInfo struct {
		File struct {
			Name string `json:"Name"`
		} `json:"file"`
		DownloadURL string `json:"download_url"`
	}
	env.Get(t, "/s/"+*linkRec.Token).Status(200).JSON(&linkInfo)
	require.Equal(t, "public.txt", linkInfo.File.Name)

	// Download via public link
	env.Get(t, "/s/"+*linkRec.Token+"/download").
		Status(200).
		Equals("public content")
}

func TestSearchFiles(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Upload files
	for _, name := range []string{"report.pdf", "notes.txt", "report-draft.pdf"} {
		env.PostText(t, "/api/files/upload?path=/"+name, "content of "+name, alice.Auth).Status(201)
	}

	// Search for "report"
	var results []files.FileMeta
	env.Get(t, "/api/files/search?q=report", alice.Auth).Status(200).JSON(&results)
	require.Equal(t, 2, len(results))
}
