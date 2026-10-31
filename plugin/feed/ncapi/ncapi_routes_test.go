package ncapi_test

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/safehttp"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
)

// Clients read the server version from /version and probe /status and /user
// while setting up an account.
func TestNewsAPI_ServerInfo(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	var version map[string]any
	env.Get(t, newsAPI+"/version", alice.Auth).Status(200).JSON(&version)
	require.Equal(t, "18.0.0", version["version"])

	var status map[string]any
	env.Get(t, newsAPI+"/status", alice.Auth).Status(200).JSON(&status)
	require.Equal(t, "18.0.0", status["version"])
	require.NotNil(t, status["warnings"])

	var u map[string]any
	env.Get(t, newsAPI+"/user", alice.Auth).Status(200).JSON(&u)
	require.Equal(t, "alice", u["userId"])

	env.Get(t, newsAPI+"/nonsense", alice.Auth).Status(404)
}

// A malformed ID used to parse as 0 and answer 200 having changed nothing.
func TestNewsAPI_BadAndMissingIDs(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	doReq(t, env, "DELETE", newsAPI+"/feeds/abc", nil, alice.Auth).Status(400)
	doReq(t, env, "PUT", newsAPI+"/feeds/abc/rename", map[string]any{"feedTitle": "x"}, alice.Auth).Status(400)
	doReq(t, env, "PUT", newsAPI+"/folders/abc", map[string]any{"name": "x"}, alice.Auth).Status(400)

	doReq(t, env, "DELETE", newsAPI+"/feeds/999", nil, alice.Auth).Status(404)
	doReq(t, env, "PUT", newsAPI+"/feeds/999/rename", map[string]any{"feedTitle": "x"}, alice.Auth).Status(404)
	doReq(t, env, "PUT", newsAPI+"/feeds/999/move", map[string]any{"folderId": 0}, alice.Auth).Status(404)
	doReq(t, env, "PUT", newsAPI+"/folders/999", map[string]any{"name": "x"}, alice.Auth).Status(404)
	doReq(t, env, "DELETE", newsAPI+"/folders/999", nil, alice.Auth).Status(404)
}

// The news API manages RSS feeds only; podcast and video subscriptions share
// the table but are not its to change.
func TestNewsAPI_LeavesOtherKindsAlone(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	res, err := db.Exec(`INSERT INTO subscriptions (kind, url, title) VALUES ('youtube', 'https://youtube.com/channel/abc', 'Channel')`)
	require.NoError(t, err)
	ytID, err := res.LastInsertId()
	require.NoError(t, err)
	id := fmtID(float64(ytID))

	doReq(t, env, "PUT", newsAPI+"/feeds/"+id+"/rename", map[string]any{"feedTitle": "x"}, alice.Auth).Status(404)
	doReq(t, env, "DELETE", newsAPI+"/feeds/"+id, nil, alice.Auth).Status(404)

	var title string
	require.NoError(t, db.QueryRow(`SELECT title FROM subscriptions WHERE id = ?`, ytID).Scan(&title))
	require.Equal(t, "Channel", title)
}

// Deleting a folder deletes its feeds, as in NextCloud News, but only the RSS
// ones.
func TestNewsAPI_DeleteFolderDeletesFeeds(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	res, err := db.Exec(`INSERT INTO folders (name) VALUES ('News')`)
	require.NoError(t, err)
	folderID, err := res.LastInsertId()
	require.NoError(t, err)
	inFolder := sql.NullInt64{Int64: folderID, Valid: true}
	createSub(t, db, "https://example.com/a.xml", inFolder)
	_, err = db.Exec(`INSERT INTO subscriptions (kind, url, folder_id) VALUES ('youtube', 'https://youtube.com/channel/abc', ?)`, folderID)
	require.NoError(t, err)

	doReq(t, env, "DELETE", newsAPI+"/folders/"+fmtID(float64(folderID)), nil, alice.Auth).Status(200)

	var rss, yt int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE kind = 'rss'`).Scan(&rss))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM subscriptions WHERE kind = 'youtube' AND folder_id IS NULL`).Scan(&yt))
	require.Equal(t, 0, rss)
	require.Equal(t, 1, yt)
}

// Moving into a folder that does not exist is a 404, not a constraint error.
func TestNewsAPI_MoveFeedToMissingFolder(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/a.xml", sql.NullInt64{})
	doReq(t, env, "PUT", newsAPI+"/feeds/"+fmtID(float64(subID))+"/move", map[string]any{"folderId": 999}, alice.Auth).Status(404)
}

// folderId 0 means the root. It used to be stored as folder 0, which does
// not exist.
func TestNewsAPI_CreateFeedAtRoot(t *testing.T) {
	safehttp.SetAllowPrivate(true) // the feed server is on loopback
	t.Cleanup(func() { safehttp.SetAllowPrivate(false) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>T</title></channel></rss>`))
	}))
	defer srv.Close()

	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")

	var resp struct {
		Feeds []map[string]any `json:"feeds"`
	}
	doReq(t, env, "POST", newsAPI+"/feeds", map[string]any{"url": srv.URL + "/feed.xml", "folderId": 0}, alice.Auth).
		Status(200).JSON(&resp)
	require.Equal(t, 1, len(resp.Feeds))
	require.Equal(t, any(float64(0)), resp.Feeds[0]["folderId"])
}

func TestNewsAPI_UpdatedStarredItems(t *testing.T) {
	env := cogtest.New(t, feed.New())
	alice := env.CreateUser(t, "alice", "pass")
	db, err := feedPlugin(t, env).DB(alice.Username)
	require.NoError(t, err)

	subID := createSub(t, db, "https://example.com/feed.xml", sql.NullInt64{})
	ids := insertTextItems(t, db, subID, "A", "B")
	_, err = db.Exec(`UPDATE items SET is_starred = 1 WHERE id = ?`, ids[0])
	require.NoError(t, err)

	var resp apiItems
	env.Get(t, newsAPI+"/items/updated?lastModified=0&type=2", alice.Auth).Status(200).JSON(&resp)
	require.Equal(t, 1, len(resp.Items))
	require.Equal(t, "A", resp.Items[0]["title"])
}
