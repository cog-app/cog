package rss_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/rss"
)

const newsAPI = "/index.php/apps/news/api/v1-2"

func doReq(t testing.TB, env *cogtest.Env, method, path string, body any, opts ...cogtest.HTTPOpt) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = strings.NewReader(string(data))
	}
	req := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	req.Header.Set("Content-Type", "application/json")
	return env.Do(t, req, opts...)
}

func fmtID(f float64) string { return fmt.Sprintf("%d", int64(f)) }

func TestNewsAPI_Version(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	var resp map[string]any
	env.Get(t, newsAPI+"/", alice.Auth).Status(200).JSON(&resp)
	levels := resp["apiLevels"].([]any)
	require.Equal(t, "v1-2", levels[0].(string))
}

func TestNewsAPI_Folders(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create folder.
	var createResp struct {
		Folders []map[string]any `json:"folders"`
	}
	doReq(t, env, "POST", newsAPI+"/folders", map[string]any{
		"name": "Tech News",
	}, alice.Auth).Status(200).JSON(&createResp)
	require.Equal(t, 1, len(createResp.Folders))
	require.Equal(t, "Tech News", createResp.Folders[0]["name"])

	// List folders.
	var listResp struct {
		Folders []map[string]any `json:"folders"`
	}
	env.Get(t, newsAPI+"/folders", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Folders))

	// Rename folder.
	fid := fmtID(listResp.Folders[0]["id"].(float64))
	doReq(t, env, "PUT", newsAPI+"/folders/"+fid, map[string]any{
		"name": "Science News",
	}, alice.Auth).Status(200)

	env.Get(t, newsAPI+"/folders", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, "Science News", listResp.Folders[0]["name"])

	// Delete folder.
	doReq(t, env, "DELETE", newsAPI+"/folders/"+fid, nil, alice.Auth).Status(200)
	env.Get(t, newsAPI+"/folders", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 0, len(listResp.Folders))
}

func TestNewsAPI_FeedCRUD(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Create a feed directly in the store since the API tries to resolve the URL.
	plug, _ := env.Providers.Plugins.Plugin("rss")
	rp := plug.(*rss.Plugin)
	db, err := rp.DB(alice.Username)
	require.NoError(t, err)

	feed, err := rss.CreateFeed(db, "https://example.com/feed.xml", sql.NullInt64{})
	require.NoError(t, err)

	// List feeds via API.
	var listResp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Feeds))

	// Delete feed.
	doReq(t, env, "DELETE", newsAPI+"/feeds/"+fmtID(float64(feed.ID)), nil, alice.Auth).Status(200)
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 0, len(listResp.Feeds))
}

func TestNewsAPI_Items(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("rss")
	rp := plug.(*rss.Plugin)
	db, err := rp.DB(alice.Username)
	require.NoError(t, err)

	feed, err := rss.CreateFeed(db, "https://blog.example.com/rss", sql.NullInt64{})
	require.NoError(t, err)

	// Insert items directly.
	for _, title := range []string{"Post A", "Post B", "Post C"} {
		_, err := db.Exec(`INSERT INTO items (feed_id, guid, title, url, content_html) VALUES (?, ?, ?, ?, ?)`,
			feed.ID, "guid-"+title, title, "https://blog.example.com/"+title, "<p>"+title+"</p>")
		require.NoError(t, err)
	}

	// List items via API.
	var itemsResp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)
	require.Equal(t, 3, len(itemsResp.Items))

	// All should be unread.
	for _, item := range itemsResp.Items {
		require.Equal(t, true, item["unread"])
	}

	// Mark one as read.
	itemID := fmtID(itemsResp.Items[0]["id"].(float64))
	doReq(t, env, "PUT", newsAPI+"/items/"+itemID+"/read", nil, alice.Auth).Status(200)

	// Verify it's now read.
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)
	found := false
	for _, item := range itemsResp.Items {
		if fmtID(item["id"].(float64)) == itemID {
			require.Equal(t, false, item["unread"])
			found = true
		}
	}
	require.True(t, found)
}

func TestNewsAPI_StarItem(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("rss")
	rp := plug.(*rss.Plugin)
	db, err := rp.DB(alice.Username)
	require.NoError(t, err)

	feed, err := rss.CreateFeed(db, "https://example.com/rss", sql.NullInt64{})
	require.NoError(t, err)

	res, err := db.Exec(`INSERT INTO items (feed_id, guid, title, url, content_html) VALUES (?, ?, ?, ?, ?)`,
		feed.ID, "star-guid", "Star Me", "https://example.com/star", "<p>star</p>")
	require.NoError(t, err)
	itemID, _ := res.LastInsertId()

	// Star it.
	doReq(t, env, "PUT", newsAPI+"/items/"+fmtID(float64(itemID))+"/star", nil, alice.Auth).Status(200)

	// Verify.
	var itemsResp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)
	require.Equal(t, true, itemsResp.Items[0]["starred"])
}

func TestRSSPage(t *testing.T) {
	env := cogtest.New(t, rss.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/rss/", alice.Auth).Status(200)
}
