package rss_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/rss"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, rss.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "empty", "pass")
	env.CreateUser(t, "settings", "pass")
	env.CreateUser(t, "starred", "pass")

	t.Run("EmptyState", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "empty", "pass")
		b.Navigate(t, "/apps/rss/")
		b.WaitVisible(t, "#rss-sidebar")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/rss/settings")
		b.WaitVisible(t, "#url")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("StarredPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "starred", "pass")
		b.Navigate(t, "/apps/rss/starred")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
