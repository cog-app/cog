package calendar_test

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// tzEnv sets up a user with a default calendar and returns its DB and ID.
func tzEnv(t *testing.T) (*cogtest.Env, *cogtest.User, *sql.DB, int64) {
	t.Helper()
	env, alice, _, db, cal := calEnv(t)
	return env, alice, db, cal.ID
}

const denverICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:denver-1\r\nSUMMARY:Imported\r\n" +
	"DTSTART;TZID=America/Denver:20261025T090000\r\nDTEND;TZID=America/Denver:20261025T100000\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// 09:00 MDT on 2026-10-25.
var denverStart = time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC).Unix()

func TestCalDAVPutHonoursTZID(t *testing.T) {
	env, alice, db, calID := tzEnv(t)

	req := cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/denver-1.ics", strings.NewReader(denverICS))
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	env.Do(t, req, alice.Auth).Status(201)

	ev, err := calendar.GetEventByUID(db, calID, "denver-1")
	require.NoError(t, err)
	assert.Equal(t, denverStart, ev.DTStart)
	assert.Equal(t, denverStart+3600, ev.DTEnd)
}

func TestCreateEventUsesBrowserZone(t *testing.T) {
	env, alice, db, calID := tzEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)

	form := url.Values{
		"calendar_id":    {fmt.Sprint(calID)},
		"summary":        {"Standup"},
		"datetime_start": {"2026-10-25T09:00"},
		"datetime_end":   {"2026-10-25T10:00"},
		"tz":             {"America/Denver"},
	}
	env.PostForm(t, "/apps/calendar/events", form, cookie, csrf, cogtest.StopRedirect).Status(303)

	events, err := calendar.ListEvents(db, calID)
	require.NoError(t, err)
	require.Equal(t, 1, len(events))
	assert.Equal(t, denverStart, events[0].DTStart)
	full, err := calendar.GetEvent(db, events[0].ID)
	require.NoError(t, err)
	ics := rendered(t, full)
	assert.True(t, strings.Contains(ics, "DTSTART;TZID=America/Denver:20261025T090000"), ics)

	// The edit modal shows the event in its own zone.
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d", events[0].ID), alice.Auth).Status(200).
		Contains(`value="2026-10-25T09:00"`).Contains(`name="tz" list="cal-timezones" value="America/Denver"`)

	form.Set("tz", "Mars/Olympus")
	env.PostForm(t, "/apps/calendar/events", form, cookie, csrf, cogtest.StopRedirect).Status(400)
}

func TestUpdateEventKeepsAlarmsAndMovesCalendar(t *testing.T) {
	env, alice, cp, db, cal := calEnv(t)
	calID := cal.ID
	cookie, csrf := sessionAuth(t, env, alice.ID)
	work, err := cp.CreateCalendar(alice.ID, "work", "Work", "", "", "America/Denver")
	require.NoError(t, err)

	ics := strings.Replace(denverICS, "END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT", 1)
	ev := saveICS(t, env, db, cal, ics)

	form := url.Values{
		"calendar_id":    {fmt.Sprint(work.ID)},
		"summary":        {"Moved"},
		"datetime_start": {"2026-10-25T09:00"},
		"datetime_end":   {"2026-10-25T10:00"},
		"tz":             {"America/Denver"},
	}
	env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d", ev.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)

	old, err := calendar.ListEvents(db, calID)
	require.NoError(t, err)
	assert.Equal(t, 0, len(old), "event left behind in the old calendar")
	moved, err := calendar.ListEvents(db, work.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(moved))
	full, err := calendar.GetEvent(db, moved[0].ID)
	require.NoError(t, err)
	assert.True(t, strings.Contains(rendered(t, full), "TRIGGER:-PT15M"), "alarm dropped on edit")
}

func TestUpdateCalendarSettings(t *testing.T) {
	env, alice, cp, db, c := calEnv(t)
	calID := c.ID
	cookie, csrf := sessionAuth(t, env, alice.ID)

	// A floating event follows the calendar's zone.
	saveICS(t, env, db, c, "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:float\r\nDTSTART:20261025T090000\r\nDTEND:20261025T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")

	form := url.Values{"name": {"Family"}, "color": {"#ff8800"}, "timezone": {"America/Denver"}}
	env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d", calID), form, cookie, csrf, cogtest.StopRedirect).Status(303)

	cal, err := cp.Calendar(alice.ID, calID)
	require.NoError(t, err)
	assert.Equal(t, "Family", cal.Name)
	assert.Equal(t, "#ff8800", cal.Color)
	assert.Equal(t, "America/Denver", cal.Timezone)

	ev, err := calendar.GetEventByUID(db, calID, "float")
	require.NoError(t, err)
	assert.Equal(t, denverStart, ev.DTStart)

	// An unknown zone is refused with a message after the redirect (PRG),
	// and changes nothing.
	form.Set("name", "Renamed")
	form.Set("timezone", "Mars/Olympus")
	env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d", calID), form, cookie, csrf, cogtest.StopRedirect).Status(303)
	cal, err = cp.Calendar(alice.ID, calID)
	require.NoError(t, err)
	assert.Equal(t, "Family", cal.Name)
	assert.Equal(t, "America/Denver", cal.Timezone)
}

func TestEventsJSONFilteredByCalendar(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	calID := cal.ID
	saveICS(t, env, db, cal, denverICS)

	body := env.Get(t, fmt.Sprintf("/apps/calendar/events.json?calendar_id=%d&start=2026-10-01T00:00:00Z&end=2026-11-01T00:00:00Z", calID), alice.Auth).Status(200).ReadBody()
	assert.True(t, strings.Contains(body, "Imported"), body)
}
