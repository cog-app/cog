package feed_test

import (
	"database/sql"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/feed"
	"git.duckfam.us/jj-software/cog/plugin/feed/syncprefs"
)

func TestBulkSubscriptions(t *testing.T) {
	env := cogtest.New(t, feed.New())
	u := env.CreateUser(t, "u", "pass")
	raw, _ := env.Providers.Plugins.Plugin(feed.PluginID)
	db, err := raw.(*feed.Plugin).DB(u.Username)
	require.NoError(t, err)
	store := feed.NewStore(db)

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	session := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	post := func(path string, form url.Values) {
		t.Helper()
		form.Set("csrf_token", csrf)
		env.PostForm(t, path, form, session, cogtest.StopRedirect).Status(http.StatusSeeOther)
	}
	bulk := func(form url.Values, ids ...int64) {
		t.Helper()
		for _, id := range ids {
			form.Add("id", strconv.FormatInt(id, 10))
		}
		post("/apps/feed/subscriptions/bulk", form)
	}

	var subs []int64
	for _, u := range []string{"https://a.example/feed", "https://b.example/feed", "https://c.example/feed"} {
		id, err := store.CreateSubscription(feed.Subscription{Kind: "rss", URL: u})
		require.NoError(t, err)
		subs = append(subs, id)
	}
	folderOf := func(id int64) sql.NullInt64 {
		t.Helper()
		s, err := store.GetSubscription(id)
		require.NoError(t, err)
		return s.FolderID
	}
	tech, err := store.CreateFolder("Tech")
	require.NoError(t, err)

	t.Run("MoveToFolder", func(t *testing.T) {
		bulk(url.Values{"action": {"move"}, "folder_id": {strconv.FormatInt(tech, 10)}}, subs[0], subs[1])
		require.Equal(t, sql.NullInt64{Int64: tech, Valid: true}, folderOf(subs[0]))
		require.Equal(t, sql.NullInt64{Int64: tech, Valid: true}, folderOf(subs[1]))
		require.False(t, folderOf(subs[2]).Valid)
	})

	t.Run("MoveToNewFolder", func(t *testing.T) {
		bulk(url.Values{"action": {"move"}, "folder_id": {"new"}, "new_folder": {" Podcasts "}}, subs[1], subs[2])
		folders, err := store.ListFolders()
		require.NoError(t, err)
		require.Equal(t, 2, len(folders))
		require.Equal(t, "Podcasts", folders[0].Name)
		require.Equal(t, sql.NullInt64{Int64: folders[0].ID, Valid: true}, folderOf(subs[2]))

		// Naming an existing folder uses it.
		bulk(url.Values{"action": {"move"}, "folder_id": {"new"}, "new_folder": {"Tech"}}, subs[2])
		require.Equal(t, sql.NullInt64{Int64: tech, Valid: true}, folderOf(subs[2]))
	})

	t.Run("MoveToNoFolder", func(t *testing.T) {
		bulk(url.Values{"action": {"move"}, "folder_id": {""}}, subs[0])
		require.False(t, folderOf(subs[0]).Valid)
	})

	t.Run("RefusesWithoutSelectionOrFolder", func(t *testing.T) {
		bulk(url.Values{"action": {"move"}, "folder_id": {strconv.FormatInt(tech, 10)}})
		bulk(url.Values{"action": {"move"}, "folder_id": {"new"}, "new_folder": {""}}, subs[0])
		bulk(url.Values{"action": {"move"}, "folder_id": {"99999"}}, subs[0])
		require.False(t, folderOf(subs[0]).Valid)
	})

	t.Run("Unsubscribe", func(t *testing.T) {
		bulk(url.Values{"action": {"delete"}}, subs[0], subs[1])
		all, err := store.ListSubscriptions()
		require.NoError(t, err)
		require.Equal(t, 1, len(all))
		require.Equal(t, subs[2], all[0].ID)
	})

	t.Run("SyncFolders", func(t *testing.T) {
		post("/apps/feed/settings/sync", url.Values{"gpodder_folder": {strconv.FormatInt(tech, 10)}, "ncnews_folder": {""}})
		got, err := syncprefs.Folder(db, syncprefs.GPodder)
		require.NoError(t, err)
		require.Equal(t, sql.NullInt64{Int64: tech, Valid: true}, got)
		got, err = syncprefs.Folder(db, syncprefs.NCNews)
		require.NoError(t, err)
		require.False(t, got.Valid)
	})
}
