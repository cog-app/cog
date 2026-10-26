package calendar_test

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// seriesICS is a client's recurring event: an override, an exception,
// vendor properties, an alarm and an attendee with parameters.
const seriesICS = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Client//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:series\r\nDTSTAMP:20261001T080000Z\r\nSEQUENCE:3\r\nSUMMARY:Planning\r\nLOCATION:HQ\r\n" +
	"DESCRIPTION:Agenda\r\nX-ALT-DESC;FMTTYPE=text/html:<b>Agenda</b>\r\n" +
	"X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-TITLE=HQ:geo:1,2\r\n" +
	"DTSTART;TZID=America/Denver:20261005T090000\r\nDTEND;TZID=America/Denver:20261005T100000\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\nEXDATE;TZID=America/Denver:20261019T090000\r\n" +
	"ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED;X-NUM-GUESTS=1:mailto:bob@example.com\r\n" +
	"ATTENDEE;CUTYPE=ROOM:urn:uuid:room-1\r\n" +
	"X-KEEP-ME:yes\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nX-WR-ALARMUID:A1\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:series\r\nRECURRENCE-ID;TZID=America/Denver:20261012T090000\r\n" +
	"SUMMARY:Planning (moved)\r\nDTSTART;TZID=America/Denver:20261012T140000\r\nDTEND;TZID=America/Denver:20261012T150000\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

// TestWebEditKeepsClientData edits a client's event in the web form and
// checks that only what the form shows changed: the override, exception,
// alarm, attendee parameters, the attendee the form cannot show and unknown
// properties stay; vendor copies of the edited fields go; SEQUENCE and
// DTSTAMP move on.
func TestWebEditKeepsClientData(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	path := env.Providers.Config.BaseURL + "/remote.php/dav/calendars/alice/personal/series.ics"
	put := cogtest.NewRequest(t, "PUT", path, strings.NewReader(seriesICS))
	env.Do(t, put, alice.Auth).Status(201)

	ev, err := calendar.GetEventByUID(db, cal.ID, "series")
	require.NoError(t, err)
	form := url.Values{
		"calendar_id":    {fmt.Sprint(cal.ID)},
		"summary":        {"Planning"},
		"location":       {"Annex"},
		"description":    {"Agenda"},
		"datetime_start": {"2026-10-05T09:00"},
		"datetime_end":   {"2026-10-05T10:00"},
		"tz":             {"America/Denver"},
		"rrule_freq":     {"CUSTOM"},
		"rrule":          {"FREQ=WEEKLY;COUNT=6"},
		"attendee_email": {"bob@example.com"},
		"attendee_name":  {"Bob"},
		"attendee_role":  {"REQ-PARTICIPANT"},
	}
	env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d", ev.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)

	body := env.Do(t, cogtest.NewRequest(t, "GET", path, nil), alice.Auth).Status(200).ReadBody()
	for _, want := range []string{
		"LOCATION:Annex\r\n",
		"SEQUENCE:4\r\n",
		"DTSTART;TZID=America/Denver:20261005T090000\r\n",
		"RRULE:FREQ=WEEKLY;COUNT=6\r\n",
		"EXDATE;TZID=America/Denver:20261019T090000\r\n",
		"X-NUM-GUESTS=1:mailto:bob@example.com\r\n",
		"ATTENDEE;CUTYPE=ROOM:urn:uuid:room-1\r\n",
		"X-KEEP-ME:yes\r\n",
		"X-ALT-DESC;FMTTYPE=text/html:<b>Agenda</b>\r\n",
		"X-WR-ALARMUID:A1\r\n",
		"RECURRENCE-ID;TZID=America/Denver:20261012T090000\r\n",
		"SUMMARY:Planning (moved)\r\n",
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "X-APPLE-STRUCTURED-LOCATION")
	assert.NotContains(t, body, "DTSTAMP:20261001T080000Z\r\nSEQUENCE")

	// A change to the description alone is not a scheduling change, and
	// drops the stale HTML copy.
	form.Set("description", "New agenda")
	env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d", ev.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)
	body = env.Do(t, cogtest.NewRequest(t, "GET", path, nil), alice.Auth).Status(200).ReadBody()
	assert.Contains(t, body, "SEQUENCE:4\r\n")
	assert.Contains(t, body, "DESCRIPTION:New agenda\r\n")
	assert.NotContains(t, body, "X-ALT-DESC")
}

// TestEventsJSONShowsOverrides shows each instance of a series: the override
// in place of the occurrence it moves, and no instance on the exception.
func TestEventsJSONShowsOverrides(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	saveICS(t, env, db, cal, seriesICS)

	var events []struct {
		Title string `json:"title"`
		Start string `json:"start"`
	}
	env.Get(t, "/apps/calendar/events.json?start=2026-10-01T00:00:00Z&end=2026-11-01T00:00:00Z", alice.Auth).Status(200).JSON(&events)
	var got []string
	for _, e := range events {
		got = append(got, e.Start+" "+e.Title)
	}
	b, _ := json.Marshal(got)
	assert.Equal(t, `["2026-10-05T15:00:00Z Planning","2026-10-12T20:00:00Z Planning (moved)","2026-10-26T15:00:00Z Planning"]`, string(b))
}
