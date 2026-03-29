package bookmarks_test

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/bookmarks"
)

const bmAPI = "/index.php/apps/bookmarks/public/rest/v2"

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func jsonReader(v any) io.Reader {
	data, _ := json.Marshal(v)
	return strings.NewReader(string(data))
}

func doReq(t testing.TB, env *cogtest.Env, method, path string, body any, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = jsonReader(body)
	}
	req := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	return env.Do(t, req, opts...)
}

func TestBookmarksAPI_CreateAndGet(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	var resp struct {
		Status string         `json:"status"`
		Item   map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url":   "https://example.com",
		"title": "Example",
		"tags":  []string{"test", "web"},
	}, alice.Auth).Status(200).JSON(&resp)

	require.Equal(t, "success", resp.Status)
	require.Equal(t, "https://example.com", resp.Item["url"])
	require.Equal(t, "Example", resp.Item["title"])

	// Get it back by ID.
	id := itoa(int64(resp.Item["id"].(float64)))
	var getResp struct {
		Status string         `json:"status"`
		Item   map[string]any `json:"item"`
	}
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).
		Status(200).JSON(&getResp)
	require.Equal(t, "Example", getResp.Item["title"])
}

func TestBookmarksAPI_List(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://one.example.com", "title": "One",
	}, alice.Auth).Status(200)
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://two.example.com", "title": "Two",
	}, alice.Auth).Status(200)

	var listResp struct {
		Bookmarks []map[string]any `json:"bookmarks"`
	}
	env.Get(t, bmAPI+"/bookmark", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 2, len(listResp.Bookmarks))
}

func TestBookmarksAPI_Update(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Old Title",
	}, alice.Auth).Status(200).JSON(&created)
	id := itoa(int64(created.Item["id"].(float64)))

	var updated struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "PUT", bmAPI+"/bookmark/"+id, map[string]any{
		"title": "New Title",
	}, alice.Auth).Status(200).JSON(&updated)
	require.Equal(t, "New Title", updated.Item["title"])
}

func TestBookmarksAPI_Delete(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Gone",
	}, alice.Auth).Status(200).JSON(&created)
	id := itoa(int64(created.Item["id"].(float64)))

	doReq(t, env, "DELETE", bmAPI+"/bookmark/"+id, nil, alice.Auth).Status(200)

	// Should be 404.
	env.Get(t, bmAPI+"/bookmark/"+id, alice.Auth).Status(404)
}

func TestBookmarksAPI_Folders(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create folder.
	var fResp struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/folder", map[string]any{
		"title": "Dev Links",
	}, alice.Auth).Status(200).JSON(&fResp)
	require.Equal(t, "Dev Links", fResp.Item["title"])

	// List folders.
	var listResp struct {
		Data []map[string]any `json:"data"`
	}
	env.Get(t, bmAPI+"/folder", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Data))

	// Delete folder.
	id := itoa(int64(fResp.Item["id"].(float64)))
	doReq(t, env, "DELETE", bmAPI+"/folder/"+id, nil, alice.Auth).Status(200)

	env.Get(t, bmAPI+"/folder", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 0, len(listResp.Data))
}

func TestBookmarksAPI_Tags(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Tagged", "tags": []string{"go", "testing"},
	}, alice.Auth).Status(200)

	var tags []string
	env.Get(t, bmAPI+"/tag", alice.Auth).Status(200).JSON(&tags)
	require.Equal(t, 2, len(tags))
}

func TestBookmarksAPI_ClickCount(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Clicky",
	}, alice.Auth).Status(200)

	// Record clicks.
	doReq(t, env, "POST", bmAPI+"/bookmark/click", map[string]any{
		"url": "https://example.com",
	}, alice.Auth).Status(200)
	doReq(t, env, "POST", bmAPI+"/bookmark/click", map[string]any{
		"url": "https://example.com",
	}, alice.Auth).Status(200)

	// List and check click count.
	var resp struct {
		Bookmarks []map[string]any `json:"bookmarks"`
	}
	env.Get(t, bmAPI+"/bookmark?url=https://example.com", alice.Auth).
		Status(200).JSON(&resp)
	require.Equal(t, 1, len(resp.Bookmarks))
	require.Equal(t, 2, int(resp.Bookmarks[0]["clickcount"].(float64)))
}

func TestBookmarksAPI_GetFolder(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create a folder.
	var created struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/folder", map[string]any{
		"title": "Reading List",
	}, alice.Auth).Status(200).JSON(&created)
	id := itoa(int64(created.Item["id"].(float64)))

	// Get it back by ID.
	var got struct {
		Item map[string]any `json:"item"`
	}
	env.Get(t, bmAPI+"/folder/"+id, alice.Auth).Status(200).JSON(&got)
	require.Equal(t, "Reading List", got.Item["title"])
}

func TestBookmarksAPI_UpdateFolder(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	var created struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "POST", bmAPI+"/folder", map[string]any{
		"title": "Old Name",
	}, alice.Auth).Status(200).JSON(&created)
	id := itoa(int64(created.Item["id"].(float64)))

	// Update the folder title.
	var updated struct {
		Item map[string]any `json:"item"`
	}
	doReq(t, env, "PUT", bmAPI+"/folder/"+id, map[string]any{
		"title": "New Name",
	}, alice.Auth).Status(200).JSON(&updated)
	require.Equal(t, "New Name", updated.Item["title"])
}

func TestBookmarksAPI_RenameTag(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create a bookmark with a tag.
	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Tagged", "tags": []string{"oldtag"},
	}, alice.Auth).Status(200)

	// Rename the tag.
	doReq(t, env, "PUT", bmAPI+"/tag", map[string]any{
		"old": "oldtag", "new": "newtag",
	}, alice.Auth).Status(200)

	// Verify the old tag is gone and new tag exists.
	var tags []string
	env.Get(t, bmAPI+"/tag", alice.Auth).Status(200).JSON(&tags)
	require.Equal(t, 1, len(tags))
	require.Equal(t, "newtag", tags[0])
}

func TestBookmarksAPI_DeleteTag(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	doReq(t, env, "POST", bmAPI+"/bookmark", map[string]any{
		"url": "https://example.com", "title": "Tagged", "tags": []string{"removeme", "keepme"},
	}, alice.Auth).Status(200)

	// Delete one tag.
	doReq(t, env, "DELETE", bmAPI+"/tag", map[string]any{
		"old": "removeme",
	}, alice.Auth).Status(200)

	var tags []string
	env.Get(t, bmAPI+"/tag", alice.Auth).Status(200).JSON(&tags)
	require.Equal(t, 1, len(tags))
	require.Equal(t, "keepme", tags[0])
}

func TestBookmarksImportExport(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Import bookmarks via store function to test the parsing and the export endpoint.
	plug, _ := env.Providers.Plugins.Plugin("bookmarks")
	bp := plug.(*bookmarks.Plugin)
	db, err := bp.DB(alice.Username)
	require.NoError(t, err)

	html := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><A HREF="https://go.dev" TAGS="go,lang">Go Website</A>
    <DT><A HREF="https://rust-lang.org" TAGS="rust">Rust Website</A>
</DL><p>`

	count, err := bookmarks.ImportNetscapeHTML(db, strings.NewReader(html))
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Verify via API.
	var listResp struct {
		Bookmarks []map[string]any `json:"bookmarks"`
	}
	env.Get(t, bmAPI+"/bookmark?limit=50", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 2, len(listResp.Bookmarks))

	// Export and verify the HTML contains the bookmarks.
	resp := env.Get(t, "/apps/bookmarks/export", alice.Auth).Status(200)
	body := resp.ReadBody()
	require.True(t, strings.Contains(body, "https://go.dev"))
	require.True(t, strings.Contains(body, "https://rust-lang.org"))
	require.True(t, strings.Contains(body, "Go Website"))
}

func TestBookmarksPage(t *testing.T) {
	env := cogtest.New(t, bookmarks.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/bookmarks/", alice.Auth).Status(200)
}
