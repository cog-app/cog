package ncapi_test

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

type apiItems struct {
	Items []map[string]any `json:"items"`
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// insertTextItems adds one unread text item per title and returns their IDs.
func insertTextItems(t testing.TB, db *sql.DB, subID int64, titles ...string) []int64 {
	t.Helper()
	var ids []int64
	for _, title := range titles {
		res, err := db.Exec(`INSERT INTO items (subscription_id, guid, type, title, url, content_html) VALUES (?, ?, 'text', ?, ?, ?)`,
			subID, "guid-"+title, title, "https://example.com/"+title, "<p>"+title+"</p>")
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		ids = append(ids, id)
	}
	return ids
}

// unreadByID returns each item's unread flag as the API reports it.
func unreadByID(t testing.TB, env *cogtest.Env, user cogtest.HTTPOpt) map[int64]bool {
	t.Helper()
	var resp apiItems
	env.Get(t, newsAPI+"/items?batchSize=-1&getRead=true", user).Status(200).JSON(&resp)
	out := make(map[int64]bool)
	for _, it := range resp.Items {
		out[int64(it["id"].(float64))] = it["unread"].(bool)
	}
	return out
}

// Clients key and deduplicate items on guidHash and fingerprint. When every
// item carried the same (empty) value, a client showed one article per feed
// and reading it marked them all read.
func TestNewsAPI_ItemHashesAreDistinct(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	ids := insertTextItems(t, db, subID, "A", "B", "C")

	var resp apiItems
	env.Get(t, newsAPI+"/items?batchSize=-1&getRead=true", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, 3, len(resp.Items))
	guidHashes := map[string]bool{}
	fingerprints := map[string]bool{}
	for _, it := range resp.Items {
		require.Equal(t, md5Hex(it["guid"].(string)), it["guidHash"].(string))
		guidHashes[it["guidHash"].(string)] = true
		fingerprints[it["fingerprint"].(string)] = true
	}
	require.Equal(t, 3, len(guidHashes))
	require.Equal(t, 3, len(fingerprints))

	doReq(t, env, "PUT", newsAPI+"/items/"+fmtID(float64(ids[1]))+"/read", nil, alice.Auth).Status(200)
	unread := unreadByID(t, env, alice.Auth)
	require.True(t, unread[ids[0]])
	require.False(t, unread[ids[1]])
	require.True(t, unread[ids[2]])
}

// API v1-2 names items by feed ID and GUID hash when starring.
func TestNewsAPI_StarByGUIDHash(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	insertTextItems(t, db, subID, "A", "B", "C")
	feedID := fmtID(float64(subID))

	starred := func() map[string]bool {
		var resp apiItems
		env.Get(t, newsAPI+"/items?batchSize=-1&getRead=true", alice.Auth).Status(200).JSON(&resp)
		out := map[string]bool{}
		for _, it := range resp.Items {
			out[it["title"].(string)] = it["starred"].(bool)
		}
		return out
	}

	doReq(t, env, "PUT", newsAPI+"/items/"+feedID+"/"+md5Hex("guid-A")+"/star", nil, alice.Auth).Status(200)
	require.Equal(t, map[string]bool{"A": true, "B": false, "C": false}, starred())

	doReq(t, env, "PUT", newsAPI+"/items/star/multiple", map[string]any{
		"items": []map[string]any{
			{"feedId": subID, "guidHash": md5Hex("guid-B")},
			{"feedId": subID, "guidHash": md5Hex("guid-C")},
		},
	}, alice.Auth).Status(200)
	require.Equal(t, map[string]bool{"A": true, "B": true, "C": true}, starred())

	doReq(t, env, "PUT", newsAPI+"/items/unstar/multiple", map[string]any{
		"items": []map[string]any{{"feedId": subID, "guidHash": md5Hex("guid-A")}},
	}, alice.Auth).Status(200)
	require.Equal(t, map[string]bool{"A": false, "B": true, "C": true}, starred())

	doReq(t, env, "PUT", newsAPI+"/items/"+feedID+"/"+md5Hex("no-such-guid")+"/star", nil, alice.Auth).Status(404)
	doReq(t, env, "PUT", newsAPI+"/items/not-a-number/read", nil, alice.Auth).Status(400)
}

// v1-3 clients send bulk IDs as itemIds.
func TestNewsAPI_BulkReadItemIDs(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	ids := insertTextItems(t, db, subID, "A", "B")

	doReq(t, env, "PUT", newsAPI+"/items/read/multiple", map[string]any{"itemIds": []int64{ids[0]}}, alice.Auth).Status(200)
	unread := unreadByID(t, env, alice.Auth)
	require.False(t, unread[ids[0]])
	require.True(t, unread[ids[1]])
}

// Mark-all and mark-folder stop at newestItemId so items the client has not
// seen stay unread.
func TestNewsAPI_MarkAllAndFolderRead(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	res, err := db.Exec(`INSERT INTO folders (name) VALUES ('News')`)
	require.NoError(t, err)
	folderID, err := res.LastInsertId()
	require.NoError(t, err)

	inFolder := createSub(t, db, "https://example.com/a.xml", sql.NullInt64{Int64: folderID, Valid: true})
	loose := createSub(t, db, "https://example.com/b.xml", sql.NullInt64{})
	f := insertTextItems(t, db, inFolder, "F1", "F2")
	l := insertTextItems(t, db, loose, "L1", "L2")

	doReq(t, env, "PUT", newsAPI+"/folders/"+fmtID(float64(folderID))+"/read", map[string]any{"newestItemId": f[1]}, alice.Auth).Status(200)
	unread := unreadByID(t, env, alice.Auth)
	require.False(t, unread[f[0]])
	require.False(t, unread[f[1]])
	require.True(t, unread[l[0]])
	require.True(t, unread[l[1]])

	// The folder still exists under its name: /read did not reach rename.
	var folders struct {
		Folders []map[string]any `json:"folders"`
	}
	env.Get(t, newsAPI+"/folders", alice.Auth).Status(200).JSON(&folders)
	require.Equal(t, "News", folders.Folders[0]["name"])

	doReq(t, env, "PUT", newsAPI+"/items/read", map[string]any{"newestItemId": l[0]}, alice.Auth).Status(200)
	unread = unreadByID(t, env, alice.Auth)
	require.False(t, unread[l[0]])
	require.True(t, unread[l[1]])
}

func TestNewsAPI_FeedUnreadCounts(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subA := createSub(t, db, "https://example.com/a.xml", sql.NullInt64{})
	subB := createSub(t, db, "https://example.com/b.xml", sql.NullInt64{})
	a := insertTextItems(t, db, subA, "A1", "A2", "A3")
	b := insertTextItems(t, db, subB, "B1")
	_, err = db.Exec(`UPDATE items SET is_read = 1 WHERE id = ?`, a[0])
	require.NoError(t, err)

	var resp struct {
		Feeds        []map[string]any `json:"feeds"`
		NewestItemID float64          `json:"newestItemId"`
	}
	env.Get(t, newsAPI+"/feeds", alice.Auth).Status(200).JSON(&resp)
	counts := map[int64]float64{}
	for _, f := range resp.Feeds {
		counts[int64(f["id"].(float64))] = f["unreadCount"].(float64)
	}
	require.Equal(t, map[int64]float64{subA: 2, subB: 1}, counts)
	require.Equal(t, float64(b[0]), resp.NewestItemID)
}

// Reading an item in the web UI must reach clients through /items/updated.
func TestNewsAPI_WebReadShowsInUpdated(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	ids := insertTextItems(t, db, subID, "A")
	_, err = db.Exec(`UPDATE items SET fetched_at = 1000`)
	require.NoError(t, err)

	require.NoError(t, feed.NewStore(db).MarkRead(ids[0], true))

	var resp apiItems
	env.Get(t, newsAPI+"/items/updated?lastModified=1001&type=3", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, false, resp.Items[0]["unread"])
}

// offset is the lowest ID the client already has, not a row count.
func TestNewsAPI_ItemsPageByID(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	ids := insertTextItems(t, db, subID, "A", "B", "C", "D", "E")

	titles := func(query string) []string {
		var resp apiItems
		env.Get(t, newsAPI+"/items?"+query, alice.Auth).Status(200).JSON(&resp)
		var out []string
		for _, it := range resp.Items {
			out = append(out, it["title"].(string))
		}
		return out
	}

	require.Equal(t, []string{"E", "D"}, titles("batchSize=2"))
	require.Equal(t, []string{"C", "B"}, titles("batchSize=2&offset="+fmtID(float64(ids[3]))))
	require.Equal(t, []string{"A"}, titles("batchSize=2&offset="+fmtID(float64(ids[1]))))
	require.Equal(t, []string{"C", "D"}, titles("batchSize=2&oldestFirst=true&offset="+fmtID(float64(ids[1]))))
}
