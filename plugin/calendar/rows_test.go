package calendar_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"testing"
	"time"

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
// DTSTAMP move on, and the attendees are scheduled.
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
		// Inviting makes the user the organizer; the invitation went by
		// email, and the room has no address Cog can deliver to.
		"ORGANIZER:/remote.php/dav/principals/users/alice/\r\n",
		"X-NUM-GUESTS=1;SCHEDULE-STATUS=1.1:mailto\r\n :bob@example.com\r\n",
		"ATTENDEE;CUTYPE=ROOM;SCHEDULE-STATUS=3.7:urn:uuid:room-1\r\n",
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

// occ is the occurrence parameter for a start of the series in Denver.
func occ(t *testing.T, local string) string {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02T15:04", local, calendar.LoadTimezone("America/Denver"))
	require.NoError(t, err)
	return fmt.Sprint(at.Unix())
}

// seriesEnv stores seriesICS over DAV and returns what the scope tests need.
func seriesEnv(t *testing.T) (*cogtest.Env, *cogtest.User, *calendar.Event, func() string, func(form url.Values) *cogtest.Response) {
	t.Helper()
	env, alice, _, db, cal := calEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	path := env.Providers.Config.BaseURL + "/remote.php/dav/calendars/alice/personal/series.ics"
	env.Do(t, cogtest.NewRequest(t, "PUT", path, strings.NewReader(seriesICS)), alice.Auth).Status(201)
	ev, err := calendar.GetEventByUID(db, cal.ID, "series")
	require.NoError(t, err)
	get := func() string {
		return env.Do(t, cogtest.NewRequest(t, "GET", path, nil), alice.Auth).Status(200).ReadBody()
	}
	post := func(form url.Values) *cogtest.Response {
		base := url.Values{
			"calendar_id": {fmt.Sprint(cal.ID)}, "summary": {"Planning"}, "location": {"HQ"}, "description": {"Agenda"},
			"tz": {"America/Denver"}, "rrule_freq": {"CUSTOM"}, "rrule": {"FREQ=WEEKLY;COUNT=6"},
			"attendee_email": {"bob@example.com"}, "attendee_name": {"Bob"}, "attendee_role": {"REQ-PARTICIPANT"},
		}
		maps.Copy(base, form)
		return env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d", ev.ID), base, cookie, csrf, cogtest.StopRedirect)
	}
	return env, alice, ev, get, post
}

func instancesJSON(t *testing.T, env *cogtest.Env, u *cogtest.User) string {
	t.Helper()
	var events []struct {
		Title string `json:"title"`
		Start string `json:"start"`
	}
	env.Get(t, "/apps/calendar/events.json?start=2026-10-01T00:00:00Z&end=2026-12-01T00:00:00Z", u.Auth).Status(200).JSON(&events)
	var got []string
	for _, e := range events {
		got = append(got, e.Start[5:13]+" "+e.Title)
	}
	return strings.Join(got, ", ")
}

func TestEditOneOccurrence(t *testing.T) {
	env, alice, ev, get, post := seriesEnv(t)
	o := occ(t, "2026-10-26T09:00")
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d?occurrence=%s", ev.ID, o), alice.Auth).Status(200).
		Contains("Apply to").Contains(`value="2026-10-26T09:00"`)
	env.Get(t, fmt.Sprintf("/apps/calendar/events/%d?occurrence=%s", ev.ID, occ(t, "2026-10-27T09:00")), alice.Auth).Status(404)

	post(url.Values{"occurrence": {o}, "scope": {"this"}, "summary": {"Special"},
		"datetime_start": {"2026-10-26T11:00"}, "datetime_end": {"2026-10-26T12:00"}}).Status(303)
	body := get()
	assert.Contains(t, body, "RECURRENCE-ID;TZID=America/Denver:20261026T090000\r\n")
	assert.Contains(t, body, "SUMMARY:Special\r\n")
	assert.Contains(t, body, "SEQUENCE:3\r\n") // the master's, untouched
	assert.Equal(t, "10-05T15 Planning, 10-12T20 Planning (moved), 10-26T17 Special, 11-02T16 Planning, 11-09T16 Planning",
		instancesJSON(t, env, alice))
}

func TestEditFollowingOccurrences(t *testing.T) {
	env, alice, _, get, post := seriesEnv(t)
	post(url.Values{"occurrence": {occ(t, "2026-11-02T09:00")}, "scope": {"following"}, "summary": {"Planning v2"},
		"datetime_start": {"2026-11-02T10:00"}, "datetime_end": {"2026-11-02T11:00"}}).Status(303)
	body := get()
	assert.Contains(t, body, "RRULE:FREQ=WEEKLY;UNTIL=20261102T155959Z\r\n")
	assert.Contains(t, body, "SEQUENCE:4\r\n")
	assert.Equal(t, "10-05T15 Planning, 10-12T20 Planning (moved), 10-26T15 Planning, 11-02T17 Planning v2, 11-09T17 Planning v2",
		instancesJSON(t, env, alice))
}

func TestEditAllFromOccurrence(t *testing.T) {
	env, alice, _, get, post := seriesEnv(t)
	// Moving one occurrence an hour later with "all" moves the series, and
	// its override and exception with it.
	post(url.Values{"occurrence": {occ(t, "2026-10-26T09:00")}, "scope": {"all"},
		"datetime_start": {"2026-10-26T10:00"}, "datetime_end": {"2026-10-26T11:00"}}).Status(303)
	body := get()
	assert.Contains(t, body, "DTSTART;TZID=America/Denver:20261005T100000\r\n")
	assert.Contains(t, body, "EXDATE;TZID=America/Denver:20261019T100000\r\n")
	assert.Contains(t, body, "RECURRENCE-ID;TZID=America/Denver:20261012T100000\r\n")
	assert.Equal(t, "10-05T16 Planning, 10-12T20 Planning (moved), 10-26T16 Planning, 11-02T17 Planning, 11-09T17 Planning",
		instancesJSON(t, env, alice))
}

func TestDeleteOccurrences(t *testing.T) {
	env, alice, ev, get, _ := seriesEnv(t)
	cookie, csrf := sessionAuth(t, env, alice.ID)
	del := func(local, scope string) {
		env.Delete(t, fmt.Sprintf("/apps/calendar/events/%d?occurrence=%s&scope=%s", ev.ID, occ(t, local), scope), cookie, csrf).Status(200)
	}
	del("2026-10-12T09:00", "this")
	body := get()
	assert.Contains(t, body, "EXDATE;TZID=America/Denver:20261019T090000,20261012T090000\r\n")
	assert.NotContains(t, body, "RECURRENCE-ID")

	del("2026-11-02T09:00", "following")
	assert.Equal(t, "10-05T15 Planning, 10-26T15 Planning", instancesJSON(t, env, alice))

	del("2026-10-26T09:00", "all")
	assert.Equal(t, "", instancesJSON(t, env, alice))
}

// TestRedefinedZoneMovesOtherEvents: a PUT that redefines a client's own
// TZID changes the times of the events already using it.
func TestRedefinedZoneMovesOtherEvents(t *testing.T) {
	env, alice, _, _, _ := calEnv(t)
	put := func(uid, offset string) {
		ics := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\n" +
			"BEGIN:VTIMEZONE\r\nTZID:Office Time\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:" + offset +
			"\r\nTZOFFSETTO:" + offset + "\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
			"BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + uid + "\r\nDTSTART;TZID=Office Time:20261005T090000\r\nDURATION:PT1H\r\n" +
			"END:VEVENT\r\nEND:VCALENDAR\r\n"
		req := cogtest.NewRequest(t, "PUT", env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/"+uid+".ics", strings.NewReader(ics))
		env.Do(t, req, alice.Auth).Status(201)
	}
	put("a", "+0300")
	assert.Equal(t, "10-05T06 a", instancesJSON(t, env, alice))
	put("b", "+0500")
	assert.Equal(t, "10-05T04 a, 10-05T04 b", instancesJSON(t, env, alice))
}

// TestEventsJSONCapsInstances: an event repeating every minute shows only
// the first 1,000 in a day's view, not all 1,440.
func TestEventsJSONCapsInstances(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	saveICS(t, env, db, cal, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:every-minute\r\nDTSTAMP:20261001T000000Z\r\nDTSTART:20261001T000000Z\r\nDURATION:PT1M\r\nRRULE:FREQ=MINUTELY\r\nSUMMARY:Tick\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")

	var events []struct {
		Start string `json:"start"`
	}
	env.Get(t, "/apps/calendar/events.json?start=2026-10-01T00:00:00Z&end=2026-10-02T00:00:00Z", alice.Auth).Status(200).JSON(&events)
	assert.Equal(t, 1000, len(events))
	assert.Equal(t, "2026-10-01T00:00:00Z", events[0].Start)
}
