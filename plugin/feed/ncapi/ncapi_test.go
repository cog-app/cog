package ncapi_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
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

// feedPlugin returns the *feed.Plugin from the env for direct DB access.
func feedPlugin(t testing.TB, env *cogtest.Env) *feed.Plugin {
	t.Helper()
	plug, ok := env.Providers.Plugins.Plugin(feed.PluginID)
	require.True(t, ok)
	return plug.(*feed.Plugin)
}

// createSub inserts an RSS subscription directly into the user's DB.
func createSub(t testing.TB, db *sql.DB, rawURL string, folderID sql.NullInt64) int64 {
	t.Helper()
	res, err := db.Exec(`
		INSERT INTO subscriptions (kind, url, folder_id) VALUES ('rss', ?, ?)`,
		rawURL, folderID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return id
}

// TestNewsAPI_Version checks that the version endpoint returns apiLevels.
func TestNewsAPI_Version(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	var resp map[string]any
	env.Get(t, newsAPI+"/", alice.Auth).Status(200).JSON(&resp)
	levels := resp["apiLevels"].([]any)
	require.Equal(t, "v1-2", levels[0].(string))
}

// TestNewsAPI_Folders exercises folder create/list/rename/delete.
func TestNewsAPI_Folders(t *testing.T) {
	env := cogtest.New(t, feed.New())
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

// TestNewsAPI_FeedCRUD exercises subscription list/delete via the feeds
// endpoint, which lists subscriptions of every kind.
func TestNewsAPI_FeedCRUD(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	// Insert an RSS subscription directly.
	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	_, err = db.Exec(`INSERT INTO subscriptions (kind, url) VALUES ('youtube', 'https://youtube.com/channel/abc')`)
	require.NoError(t, err)

	var listResp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 2, len(listResp.Feeds))

	doReq(t, env, "DELETE", newsAPI+"/feeds/"+fmtID(float64(subID)), nil, alice.Auth).Status(200)
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Feeds))
	require.Equal(t, "https://youtube.com/channel/abc", listResp.Feeds[0]["url"])
}

// TestNewsAPI_Items exercises item listing and marking read.
func TestNewsAPI_Items(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://blog.example.com/rss", sql.NullInt64{})

	// Insert text items.
	for _, title := range []string{"Post A", "Post B", "Post C"} {
		_, err := db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, ?, 'text', ?, ?, ?)`,
			subID, "guid-"+title, title, "https://blog.example.com/"+title, "<p>"+title+"</p>")
		require.NoError(t, err)
	}

	// An audio item without notes appears too; filters keep it out if wanted.
	_, err = db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, 'guid-audio', 'audio', 'Podcast Episode', 'https://example.com/ep1', '')`,
		subID)
	require.NoError(t, err)

	var itemsResp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)
	require.Equal(t, 4, len(itemsResp.Items))

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

// TestNewsAPI_StarItem exercises starring an item.
func TestNewsAPI_StarItem(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/rss", sql.NullInt64{})

	res, err := db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, 'star-guid', 'text', 'Star Me', 'https://example.com/star', '<p>star</p>')`,
		subID)
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

// TestNewsAPI_CreateFeedBadURL verifies SSRF protection and missing URL validation.
func TestNewsAPI_CreateFeedBadURL(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Private IP should be rejected with 422.
	doReq(t, env, "POST", newsAPI+"/feeds", map[string]any{
		"url": "http://127.0.0.1:9999/feed.xml",
	}, alice.Auth).Status(422)

	// Missing URL returns 400.
	doReq(t, env, "POST", newsAPI+"/feeds", map[string]any{}, alice.Auth).Status(400)
}

// TestNewsAPI_FeedWithFolder verifies that folderId is returned correctly.
func TestNewsAPI_FeedWithFolder(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	// Create a folder directly.
	res, err := db.Exec(`INSERT INTO folders (name) VALUES ('News')`)
	require.NoError(t, err)
	folderID, _ := res.LastInsertId()

	// Create subscription in that folder.
	createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{Int64: folderID, Valid: true})

	// Verify via API.
	var listResp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Feeds))
	require.Equal(t, folderID, int64(listResp.Feeds[0]["folderId"].(float64)))
}

// TestNewsAPI_MoveFeed verifies moving a subscription into a folder.
func TestNewsAPI_MoveFeed(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	// Create folder via API.
	var folderResp struct {
		Folders []map[string]any `json:"folders"`
	}
	doReq(t, env, "POST", newsAPI+"/folders", map[string]any{
		"name": "Tech",
	}, alice.Auth).Status(200).JSON(&folderResp)
	folderID := int64(folderResp.Folders[0]["id"].(float64))

	// Move subscription to the folder.
	doReq(t, env, "PUT", newsAPI+"/feeds/"+fmtID(float64(subID))+"/move", map[string]any{
		"folderId": folderID,
	}, alice.Auth).Status(200)

	// Verify via list.
	var listResp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Feeds))
	require.Equal(t, folderID, int64(listResp.Feeds[0]["folderId"].(float64)))
}

// TestNewsAPI_RenameFeed verifies renaming a subscription.
func TestNewsAPI_RenameFeed(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	doReq(t, env, "PUT", newsAPI+"/feeds/"+fmtID(float64(subID))+"/rename", map[string]any{
		"feedTitle": "My Renamed Feed",
	}, alice.Auth).Status(200)

	// Verify.
	var listResp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&listResp)
	require.Equal(t, 1, len(listResp.Feeds))
	require.Equal(t, "My Renamed Feed", listResp.Feeds[0]["title"])
}

// TestNewsAPI_MarkFeedRead verifies that all items in a subscription are marked read.
func TestNewsAPI_MarkFeedRead(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	// Insert unread text items.
	for _, title := range []string{"A", "B", "C"} {
		_, err := db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, ?, 'text', ?, ?, ?)`,
			subID, "guid-"+title, title, "https://example.com/"+title, "<p>"+title+"</p>")
		require.NoError(t, err)
	}

	// Mark all items in subscription as read.
	doReq(t, env, "PUT", newsAPI+"/feeds/"+fmtID(float64(subID))+"/read", map[string]any{}, alice.Auth).Status(200)

	// All items should now be read.
	var itemsResp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)
	for _, item := range itemsResp.Items {
		require.Equal(t, false, item["unread"])
	}
}

// TestNewsAPI_UpdatedItems verifies items/updated filtering by lastModified.
func TestNewsAPI_UpdatedItems(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	// Insert a text item with a known fetched_at.
	_, err = db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html, fetched_at) VALUES (?, 'guid-updated', 'text', 'Updated Item', 'https://example.com/updated', '<p>updated</p>', 1000)`,
		subID)
	require.NoError(t, err)

	// Query with lastModified=999 should return the item.
	var resp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items/updated?lastModified=999&type=3", alice.Auth).
		Status(200).JSON(&resp)
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, "Updated Item", resp.Items[0]["title"])

	// Query with lastModified=1001 should return nothing.
	env.Get(t, newsAPI+"/items/updated?lastModified=1001&type=3", alice.Auth).
		Status(200).JSON(&resp)
	require.Equal(t, 0, len(resp.Items))
}

// TestNewsAPI_BulkRead verifies bulk read marking.
func TestNewsAPI_BulkRead(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	p := feedPlugin(t, env)
	db, err := p.DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})

	// Insert text items.
	var itemIDs []int64
	for _, title := range []string{"X", "Y", "Z"} {
		res, err := db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, ?, 'text', ?, ?, ?)`,
			subID, "guid-"+title, title, "https://example.com/"+title, "<p>"+title+"</p>")
		require.NoError(t, err)
		id, _ := res.LastInsertId()
		itemIDs = append(itemIDs, id)
	}

	// Bulk mark first two as read.
	doReq(t, env, "PUT", newsAPI+"/items/read/multiple", map[string]any{
		"items": []int64{itemIDs[0], itemIDs[1]},
	}, alice.Auth).Status(200)

	// Verify.
	var itemsResp struct {
		Items []map[string]any `json:"items"`
	}
	env.Get(t, newsAPI+"/items?batchSize=50&getRead=true", alice.Auth).
		Status(200).JSON(&itemsResp)

	readCount := 0
	for _, item := range itemsResp.Items {
		if !item["unread"].(bool) {
			readCount++
		}
	}
	require.Equal(t, 2, readCount)
}
