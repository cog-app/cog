package calendar_test

import (
	"database/sql"
	"encoding/json"
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
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	db, err := plug.(*calendar.Plugin).DB(alice.Username)
	require.NoError(t, err)
	require.NoError(t, calendar.EnsureDefaultCalendar(db))
	cals, err := calendar.ListCalendars(db)
	require.NoError(t, err)
	return env, alice, db, cals[0].ID
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

func TestRepairEventTimesFixesOldRows(t *testing.T) {
	env, alice, db, calID := tzEnv(t)

	// Simulate a row written by the old parser: local time read as UTC.
	wrong := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC).Unix()
	_, err := db.Exec(`INSERT INTO events (calendar_id, uid, etag, summary, dtstart, dtend, ics_data) VALUES (?, 'denver-1', 'e', 'Imported', ?, ?, ?)`,
		calID, wrong, wrong+3600, denverICS)
	require.NoError(t, err)

	// The first request through the plugin repairs the user's rows.
	body := env.Get(t, "/apps/calendar/events.json?start=2026-10-01T00:00:00Z&end=2026-11-01T00:00:00Z", alice.Auth).Status(200).ReadBody()
	var events []struct{ Start string }
	require.NoError(t, json.Unmarshal([]byte(body), &events))
	require.Equal(t, 1, len(events))
	assert.Equal(t, "2026-10-25T15:00:00Z", events[0].Start)

	ev, err := calendar.GetEventByUID(db, calID, "denver-1")
	require.NoError(t, err)
	assert.Equal(t, denverStart, ev.DTStart)
	assert.Equal(t, "e", ev.ETag, "repair must not touch the ICS or its etag")

	// Recorded as done: a second repair is a no-op even if a row is off.
	_, err = db.Exec(`UPDATE events SET dtstart = ? WHERE uid = 'denver-1'`, wrong)
	require.NoError(t, err)
	require.NoError(t, calendar.RepairEventTimes(db))
	ev, err = calendar.GetEventByUID(db, calID, "denver-1")
	require.NoError(t, err)
	assert.Equal(t, wrong, ev.DTStart)
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
	assert.True(t, strings.Contains(events[0].ICSData, "DTSTART;TZID=America/Denver:20261025T090000"), events[0].ICSData)

	// The edit modal shows the event in its own zone.
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d", events[0].ID), alice.Auth).Status(200).
		Contains(`value="2026-10-25T09:00"`).Contains("Times in America/Denver")

	form.Set("tz", "Mars/Olympus")
	env.PostForm(t, "/apps/calendar/events", form, cookie, csrf, cogtest.StopRedirect).Status(400)
}

func TestUpdateEventKeepsAlarmsAndMovesCalendar(t *testing.T) {
	env, alice, db, calID := tzEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	work, err := calendar.CreateCalendar(db, "work", "Work", "", "", "America/Denver")
	require.NoError(t, err)

	ics := strings.Replace(denverICS, "END:VEVENT", "BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nEND:VALARM\r\nEND:VEVENT", 1)
	fields, err := calendar.ParseVEvent(ics)
	require.NoError(t, err)
	ev, err := calendar.UpsertEvent(db, calID, "denver-1", ics, fields)
	require.NoError(t, err)

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
	assert.True(t, strings.Contains(moved[0].ICSData, "TRIGGER:-PT15M"), "alarm dropped on edit")
}

func TestUpdateCalendarSettings(t *testing.T) {
	env, alice, db, calID := tzEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)

	// A floating event follows the calendar's zone.
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:float\r\nDTSTART:20261025T090000\r\nDTEND:20261025T100000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	fields, err := calendar.ParseVEvent(ics)
	require.NoError(t, err)
	_, err = calendar.UpsertEvent(db, calID, "float", ics, fields)
	require.NoError(t, err)

	form := url.Values{"name": {"Family"}, "color": {"#ff8800"}, "timezone": {"America/Denver"}}
	env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d", calID), form, cookie, csrf, cogtest.StopRedirect).Status(303)

	cal, err := calendar.GetCalendar(db, calID)
	require.NoError(t, err)
	assert.Equal(t, "Family", cal.Name)
	assert.Equal(t, "#ff8800", cal.Color)
	assert.Equal(t, "America/Denver", cal.Timezone)

	ev, err := calendar.GetEventByUID(db, calID, "float")
	require.NoError(t, err)
	assert.Equal(t, denverStart, ev.DTStart)

	form.Set("timezone", "Mars/Olympus")
	env.PostForm(t, fmt.Sprintf("/apps/calendar/calendars/%d", calID), form, cookie, csrf, cogtest.StopRedirect).Status(400)
}

func TestEventsJSONFilteredByCalendar(t *testing.T) {
	env, alice, db, calID := tzEnv(t)
	fields, err := calendar.ParseVEvent(denverICS)
	require.NoError(t, err)
	_, err = calendar.UpsertEvent(db, calID, "denver-1", denverICS, fields)
	require.NoError(t, err)

	body := env.Get(t, fmt.Sprintf("/apps/calendar/events.json?calendar_id=%d&start=2026-10-01T00:00:00Z&end=2026-11-01T00:00:00Z", calID), alice.Auth).Status(200).ReadBody()
	assert.True(t, strings.Contains(body, "Imported"), body)
}
