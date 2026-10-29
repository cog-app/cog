package dav_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// clientEvent is the kind of resource a client stores: a series with an
// override and an exception, a zone of its own, attendees with parameters,
// an alarm, and properties and parameters Cog does not model.
const clientEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Apple Inc.//macOS 15//EN\r\nCALSCALE:GREGORIAN\r\n" +
	"BEGIN:VTIMEZONE\r\nTZID:Office Time\r\nBEGIN:STANDARD\r\nDTSTART:19700101T000000\r\nTZOFFSETFROM:+0300\r\nTZOFFSETTO:+0300\r\nTZNAME:OFF\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\nUID:series-1\r\nDTSTAMP:20261001T080000Z\r\nSEQUENCE:2\r\n" +
	"SUMMARY;LANGUAGE=en:Planning\r\nLOCATION:HQ\r\n" +
	"X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-TITLE=HQ:geo:1,2\r\n" +
	"DTSTART;TZID=Office Time:20261005T090000\r\nDTEND;TZID=Office Time:20261005T100000\r\n" +
	"RRULE:FREQ=WEEKLY;COUNT=6\r\nEXDATE;TZID=Office Time:20261019T090000\r\n" +
	"ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED;X-NUM-GUESTS=1:mailto:bob@example.com\r\n" +
	"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Reminder\r\nX-WR-ALARMUID:A1\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:series-1\r\nRECURRENCE-ID;TZID=Office Time:20261012T090000\r\nDTSTAMP:20261001T080000Z\r\n" +
	"SUMMARY:Planning (moved)\r\nDTSTART;TZID=Office Time:20261012T140000\r\nDTEND;TZID=Office Time:20261012T150000\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func TestEventRowsRoundTripOverDAV(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)
	path := cal + "alice/personal/series-1.ics"

	req(t, env, alice, "PUT", path, clientEvent).Status(201)
	got := req(t, env, alice, "GET", path, "").Status(200)
	body, etag := got.ReadBody(), got.Header.Get("ETag")
	for _, want := range []string{
		"PRODID:-//Apple Inc.//macOS 15//EN\r\nCALSCALE:GREGORIAN\r\n",
		"TZID:Office Time\r\n",
		"SUMMARY;LANGUAGE=en:Planning\r\n",
		"X-APPLE-STRUCTURED-LOCATION;VALUE=URI;X-TITLE=HQ:geo:1,2\r\n",
		"DTSTART;TZID=Office Time:20261005T090000\r\n",
		"EXDATE;TZID=Office Time:20261019T090000\r\n",
		"X-NUM-GUESTS=1:mailto:bob@example.com\r\n",
		"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n",
		"X-WR-ALARMUID:A1\r\n",
		"RECURRENCE-ID;TZID=Office Time:20261012T090000\r\n",
		"SUMMARY:Planning (moved)\r\n",
	} {
		assert.Contains(t, body, want)
	}
	assert.Equal(t, body, req(t, env, alice, "GET", path, "").Status(200).ReadBody())

	// What Cog serves, stored again, is served unchanged.
	again := req(t, env, alice, "PUT", path, body, "If-Match", etag).Status(204)
	assert.True(t, again.Header.Get("ETag") != "", "no ETag for an unchanged representation")
	assert.Equal(t, body, req(t, env, alice, "GET", path, "").Status(200).ReadBody())

	// A later PUT may name the zone without defining it again.
	other := strings.Replace(clientEvent[strings.Index(clientEvent, "BEGIN:VEVENT"):], "series-1", "series-2", -1)
	other = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\n" + other
	req(t, env, alice, "PUT", cal+"alice/personal/series-2.ics", other).Status(201)
	assert.Contains(t, req(t, env, alice, "GET", cal+"alice/personal/series-2.ics", "").Status(200).ReadBody(),
		"BEGIN:VTIMEZONE\r\nTZID:Office Time\r\n")
}

func TestTimezonesByReference(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)

	assert.Contains(t, req(t, env, alice, "OPTIONS", cal+"alice/personal/", "").Header.Get("DAV"), "calendar-no-timezone")

	// A standard TZID needs no VTIMEZONE in a PUT.
	denver := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:d\r\nSUMMARY:D\r\n" +
		"DTSTART;TZID=America/Denver:20261025T090000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	req(t, env, alice, "PUT", cal+"alice/personal/d.ics", denver).Status(201)
	req(t, env, alice, "PUT", cal+"alice/personal/series-1.ics", clientEvent).Status(201)

	full := req(t, env, alice, "GET", cal+"alice/personal/d.ics", "").Status(200).ReadBody()
	assert.Contains(t, full, "BEGIN:VTIMEZONE\r\nTZID:America/Denver\r\n")
	short := req(t, env, alice, "GET", cal+"alice/personal/d.ics", "", "CalDAV-Timezones", "F").Status(200).ReadBody()
	assert.NotContains(t, short, "VTIMEZONE")
	assert.Contains(t, short, "DTSTART;TZID=America/Denver:20261025T090000")

	// A client's own zone is always sent: nothing else defines it.
	body := req(t, env, alice, "REPORT", cal+"alice/personal/", `<?xml version="1.0"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop>
<D:href>/remote.php/dav/calendars/alice/personal/series-1.ics</D:href><D:href>/remote.php/dav/calendars/alice/personal/d.ics</D:href>
</C:calendar-multiget>`, "CalDAV-Timezones", "F").Status(207).ReadBody()
	assert.Contains(t, body, "TZID:Office Time")
	assert.NotContains(t, body, "TZID:America/Denver\r\n")
}

// TestTimeRangeQueryRecurring finds a series by an occurrence long after its
// first, and leaves out a finished one.
func TestTimeRangeQueryRecurring(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)
	put := func(uid, props string) {
		req(t, env, alice, "PUT", cal+"alice/personal/"+uid+".ics", "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nBEGIN:VEVENT\r\nUID:"+uid+
			"\r\nSUMMARY:"+uid+"\r\n"+props+"END:VEVENT\r\nEND:VCALENDAR\r\n").Status(201)
	}
	put("forever", "DTSTART:20200106T090000Z\r\nDURATION:PT1H\r\nRRULE:FREQ=WEEKLY\r\n")
	put("finished", "DTSTART:20200106T090000Z\r\nDURATION:PT1H\r\nRRULE:FREQ=WEEKLY;COUNT=3\r\n")
	put("single", "DTSTART:20261012T090000Z\r\nDTEND:20261012T100000Z\r\n")
	body := req(t, env, alice, "REPORT", cal+"alice/personal/", `<?xml version="1.0"?>
<C:calendar-query xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><D:getetag/></D:prop>
<C:filter><C:comp-filter name="VCALENDAR"><C:comp-filter name="VEVENT">
<C:time-range start="20261001T000000Z" end="20261101T000000Z"/></C:comp-filter></C:comp-filter></C:filter></C:calendar-query>`, "Depth", "1").Status(207).ReadBody()
	assert.Contains(t, body, "forever.ics")
	assert.Contains(t, body, "single.ics")
	assert.NotContains(t, body, "finished.ics")
}

func TestFreeBusyRedactsRows(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	c := personal(t, env, alice)
	req(t, env, alice, "PUT", cal+"alice/personal/series-1.ics", clientEvent).Status(201)
	share(t, env, alice, bob, c.ID, 8) // FreeBusy
	body := req(t, env, bob, "GET", cal+"alice/personal/series-1.ics", "").Status(200).ReadBody()
	require.True(t, strings.Contains(body, "SUMMARY:Busy"), body)
	for _, gone := range []string{"Planning", "HQ", "bob@example.com", "X-APPLE", "VALARM"} {
		assert.NotContains(t, body, gone)
	}
	// Times and exceptions remain, so busy time is right.
	assert.Contains(t, body, "EXDATE;TZID=Office Time:20261019T090000")
	assert.Contains(t, body, "RECURRENCE-ID;TZID=Office Time:20261012T090000")
}
