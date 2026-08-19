package gallery_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/gallery"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, gallery.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "page", "pass")
	env.CreateUser(t, "settings", "pass")

	t.Run("PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/")
		b.WaitText(t, "Timeline")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/gallery/settings")
		b.WaitText(t, "Gallery Settings")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("AlbumsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/albums")
		b.WaitText(t, "Albums")
		b.Snapshot(t)
	})

	t.Run("StarredPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/starred")
		b.WaitText(t, "Starred")
		b.Snapshot(t)
	})

	t.Run("VideosPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/videos")
		b.WaitText(t, "Videos")
		b.Snapshot(t)
	})

	t.Run("PeoplePage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/people")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("PeoplePageWithPeople", func(t *testing.T) {
		user := env.CreateUser(t, "faces", "pass")
		seedPeople(t, env, user.Username, 6)

		b := eng.Start(t)
		b.Login(t, "faces", "pass")
		b.Navigate(t, "/apps/gallery/people")
		b.WaitText(t, "Unnamed")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("TagsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/tags")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DuplicatesPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/gallery/duplicates")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// seedPeople gives a user n people sharing one photo, half of them named. The
// face thumbnails 404 — the media row points at no real file — which is what we
// want for a layout snapshot: the tiles size from CSS, not from image data.
func seedPeople(t *testing.T, env *cogtest.Env, username string, n int) {
	t.Helper()
	db, err := env.Providers.Plugins.PluginDB(username, "gallery")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO media (file_id, file_path, file_etag, mime_type, width, height, date_indexed)
		VALUES (1, '/Photos/group.jpg', 'etag', 'image/jpeg', 1920, 1080, 1700000000)`)
	require.NoError(t, err)

	names := []string{"Dieter", "Ada", "Grace"}
	for i := range n {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		res, err := db.Exec(`INSERT INTO people (name, visible) VALUES (?, 1)`, name)
		require.NoError(t, err)
		personID, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO faces (media_id, person_id, x, y, w, h, confidence)
			VALUES (1, ?, 0.1, 0.1, 0.2, 0.2, 5.0)`, personID)
		require.NoError(t, err)
	}
}
