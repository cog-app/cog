package calendar_test

import (
	"strconv"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/testing/webtest"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

func TestBrowser(t *testing.T) {

	t.Run("PageLoads", func(t *testing.T) {
		eng := browserEnv(t, "page")
		b := eng.Start(t)
		b.Login(t, "page", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsPage", func(t *testing.T) {
		eng := browserEnv(t, "settings")
		b := eng.Start(t)
		b.Login(t, "settings", "pass")
		b.Navigate(t, "/apps/calendar/settings")
		b.WaitVisible(t, "main")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateEvent", func(t *testing.T) {
		eng := browserEnv(t, "create")
		b := eng.Start(t)
		b.Login(t, "create", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		b.Click(t, ".fc-newEvent-button")
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
		eng := browserEnv(t, "delete")
		b := eng.Start(t)
		b.Login(t, "delete", "pass")
		b.Navigate(t, "/apps/calendar/")
		b.WaitVisible(t, "#calendar")
		// Create an event first.
		b.Click(t, ".fc-newEvent-button")
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
		eng := browserEnv(t, "newcal")
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

	// The populated subtests look at March 2025, a year before the test
	// clock's March 2026, so the event dialog shows dates with their year.

	t.Run("PopulatedMonth", func(t *testing.T) {
		eng := populatedEnv(t, "month")
		b := eng.Start(t)
		b.Login(t, "month", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-01")
		b.WaitText(t, "Deploy Freeze")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("WeekView", func(t *testing.T) {
		eng := populatedEnv(t, "week")
		b := eng.Start(t)
		b.Login(t, "week", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-10")
		b.WaitVisible(t, ".fc-event")
		b.Click(t, ".fc-timeGridWeek-button")
		b.WaitVisible(t, ".fc-timegrid-event")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("DayView", func(t *testing.T) {
		eng := populatedEnv(t, "day")
		b := eng.Start(t)
		b.Login(t, "day", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-13")
		b.WaitVisible(t, ".fc-event")
		b.Click(t, ".fc-timeGridDay-button")
		b.WaitVisible(t, ".fc-timegrid-event")
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("EventDetail", func(t *testing.T) {
		eng := populatedEnv(t, "detail")
		b := eng.Start(t)
		b.Login(t, "detail", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-01")
		b.WaitText(t, "Quarterly planning")
		b.ClickText(t, "Quarterly planning")
		b.WaitVisible(t, `#event-modal [hx-delete]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("RecurringEventDetail", func(t *testing.T) {
		eng := populatedEnv(t, "recur")
		b := eng.Start(t)
		b.Login(t, "recur", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-01")
		b.WaitText(t, "Team Standup")
		b.ClickText(t, "Team Standup")
		b.WaitVisible(t, `#em-scope`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("CreateEventAllDayRecurring", func(t *testing.T) {
		eng := populatedEnv(t, "newform")
		b := eng.Start(t)
		b.Login(t, "newform", "pass")
		b.Navigate(t, "/apps/calendar/?date=2025-03-01")
		b.WaitText(t, "Deploy Freeze")
		// A click on a day starts an event at 9:00 that day.
		b.Click(t, `.fc-daygrid-day[data-date="2025-03-20"] .fc-daygrid-day-frame`)
		b.WaitVisible(t, "#ce-summary")
		b.Click(t, "#ce-allday")
		b.Select(t, "#ce-freq", `[value="WEEKLY"]`)
		b.WaitVisible(t, "#create-modal [data-byday]")
		b.Click(t, `#create-modal [data-add-attendee]`)
		b.WaitVisible(t, `#create-modal input[name="attendee_email"]`)
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})

	t.Run("SettingsMultipleCalendars", func(t *testing.T) {
		eng := populatedEnv(t, "multi")
		b := eng.Start(t)
		b.Login(t, "multi", "pass")
		b.Navigate(t, "/apps/calendar/settings")
		b.WaitText(t, "Family")
		// A created feed is not shown here: its address carries the test
		// server's port, which the golden does not mask.
		b.Snapshot(t)
		b.Snapshot(t, webtest.Mobile)
	})
}

// populatedEnv is browserEnv with three calendars and a week of events in
// March 2025: a weekly series, overlapping meetings, an event with attendees
// and a long title, a multi-day all-day event, and a crowded day.
func populatedEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, calendar.New())
	env.CreateUser(t, "admin", "pass")
	u := env.CreateUser(t, username, "pass")
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	db, err := cp.DB(username)
	require.NoError(t, err)
	cals, err := cp.Calendars(u.ID)
	require.NoError(t, err)
	personal := &cals[0]
	work, err := cp.CreateCalendar(u.ID, "work", "Work", "#e64a19", "", "America/New_York")
	require.NoError(t, err)
	family, err := cp.CreateCalendar(u.ID, "family", "Family", "#43a047", "", "")
	require.NoError(t, err)

	vevent := func(cal *calendar.Calendar, uid, props string) {
		ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid +
			"\r\nDTSTAMP:20250101T000000Z\r\n" + props + "END:VEVENT\r\nEND:VCALENDAR\r\n"
		_, err := calendar.SaveICS(db, cal, ics, env.Now())
		require.NoError(t, err)
	}
	vevent(personal, "standup", "SUMMARY:Team Standup\r\nLOCATION:Zoom\r\n"+
		"DTSTART:20250303T093000Z\r\nDTEND:20250303T094500Z\r\nRRULE:FREQ=WEEKLY;BYDAY=MO,WE,FR;UNTIL=20250331T000000Z\r\n")
	vevent(personal, "dentist", "SUMMARY:Dentist Appointment\r\nLOCATION:123 Main St\r\n"+
		"DTSTART:20250311T140000Z\r\nDTEND:20250311T150000Z\r\n")
	vevent(work, "planning", "SUMMARY:Quarterly planning with the whole product and engineering leadership group\r\n"+
		"LOCATION:Conference Room B\\, 4th floor\\, North Building\r\n"+
		"DESCRIPTION:Bring the roadmap draft and the hiring plan. We will go through every team's commitments for the next quarter.\r\n"+
		"DTSTART:20250312T100000Z\r\nDTEND:20250312T113000Z\r\n"+
		"ORGANIZER;CN=Erin Manager:mailto:erin@example.com\r\n"+
		"ATTENDEE;CN=Bob Jones;ROLE=REQ-PARTICIPANT;PARTSTAT=ACCEPTED:mailto:bob.jones@example.com\r\n"+
		"ATTENDEE;CN=Carol White;ROLE=OPT-PARTICIPANT;PARTSTAT=TENTATIVE:mailto:carol@example.com\r\n"+
		"ATTENDEE;ROLE=REQ-PARTICIPANT;PARTSTAT=NEEDS-ACTION:mailto:maximilian.featherstonehaugh@international-widgets.example.com\r\n")
	for i, s := range []string{"Design review", "Vendor call", "Interview: backend"} {
		vevent(work, "overlap-"+strconv.Itoa(i), "SUMMARY:"+s+"\r\n"+
			"DTSTART:20250313T13"+strconv.Itoa(i*2)+"000Z\r\nDTEND:20250313T143000Z\r\n")
	}
	vevent(work, "freeze", "SUMMARY:Deploy Freeze\r\nDTSTART;VALUE=DATE:20250313\r\nDTEND;VALUE=DATE:20250318\r\n")
	vevent(family, "birthday", "SUMMARY:Mom's Birthday\r\nDTSTART;VALUE=DATE:20250315\r\nDTEND;VALUE=DATE:20250316\r\n")
	for i, s := range []string{"School run", "Groceries", "Piano lesson", "Soccer practice", "Dinner with the Wilsons"} {
		vevent(family, "busy-"+strconv.Itoa(i), "SUMMARY:"+s+"\r\n"+
			"DTSTART:20250314T"+strconv.Itoa(10+2*i)+"0000Z\r\nDTEND:20250314T"+strconv.Itoa(11+2*i)+"0000Z\r\n")
	}

	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
}

// browserEnv starts a server and browser of its own for one subtest, with one
// user. Collection IDs are global, so a shared server would give a subtest
// different IDs, and a different golden, depending on which others ran.
func browserEnv(t *testing.T, username string) *webtest.Engine {
	t.Helper()
	env := cogtest.New(t, calendar.New())
	// The first user is made an admin; the one under test is not.
	env.CreateUser(t, "admin", "pass")
	env.CreateUser(t, username, "pass")
	eng := webtest.New(env)
	t.Cleanup(eng.Close)
	return eng
}
