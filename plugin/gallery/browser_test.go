package gallery_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
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
