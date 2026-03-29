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
	env.CreateUser(t, "delete", "pass")
	env.CreateUser(t, "newcal", "pass")

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

	t.Run("DeleteEvent", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		// Create an event first.
		b.Eval(t, `document.getElementById('create-modal').style.display='flex'`)
		b.WaitVisible(t, "#ce-summary")
		b.Type(t, "#ce-summary", "Delete Me")
		b.Eval(t, `document.getElementById('ce-start').value = '2026-03-14T14:00'`)
		b.Eval(t, `document.getElementById('ce-end').value = '2026-03-14T15:00'`)
		b.Click(t, `#create-modal button[type="submit"]`)
		b.WaitVisible(t, "#calendar")
		// Click the event to open the detail modal.
		b.Click(t, `.fc-event`)
		b.WaitVisible(t, `#event-modal [hx-delete]`)
		b.AcceptConfirms(t)
		b.Click(t, `#event-modal [hx-delete]`)
		b.WaitNotVisible(t, `#event-modal [hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateCalendar", func(t *testing.T) {
		b := eng.Start(t)
		b.Login(t, "newcal", "pass")
		b.Navigate(t, "/apps/calendar/settings")
		b.WaitVisible(t, `input[name="name"]`)
		b.Type(t, `input[name="name"]`, "Work Calendar")
		b.Click(t, `form[action="/apps/calendar/calendars"] button[type="submit"]`)
		b.WaitText(t, "Work Calendar")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}
