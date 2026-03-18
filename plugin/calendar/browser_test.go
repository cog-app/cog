package calendar_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

func TestBrowser(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	eng := webtest.New(env)
	t.Cleanup(eng.Close)

	env.CreateUser(t, "page", "pass")
	env.CreateUser(t, "settings", "pass")
	env.CreateUser(t, "create", "pass")

	t.Run("PageLoads", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/calendar/settings")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateEvent", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		b.Eval(t, `document.getElementById('create-modal').style.display='flex'`)
		b.WaitVisible(t, "#ce-summary")
		b.Type(t, "#ce-summary", "Test Event")
		b.Eval(t, `document.getElementById('ce-start').value = '2026-03-14T10:00'`)
		b.Eval(t, `document.getElementById('ce-end').value = '2026-03-14T11:00'`)
		b.Click(t, `#create-modal button[type="submit"]`)
		b.WaitVisible(t, "#calendar")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
