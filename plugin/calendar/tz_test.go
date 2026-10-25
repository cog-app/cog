package calendar

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func vevent(props ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:tz-test\r\n" +
		strings.Join(props, "\r\n") + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestParseVEventTZID(t *testing.T) {
	denver := LoadTimezone("America/Denver")
	require.True(t, denver != nil, "tzdata available")
	// 09:00 MDT on 2026-10-25 is 15:00 UTC.
	want := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)

	cases := map[string]string{
		"iana":            "DTSTART;TZID=America/Denver:20261025T090000",
		"quoted":          `DTSTART;TZID="America/Denver":20261025T090000`,
		"windows":         "DTSTART;TZID=Mountain Standard Time:20261025T090000",
		"mozilla prefix":  "DTSTART;TZID=/mozilla.org/20050126_1/America/Denver:20261025T090000",
		"lowercase param": "DTSTART;tzid=America/Denver:20261025T090000",
	}
	for name, prop := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := ParseVEvent(vevent(prop))
			require.NoError(t, err)
			assert.True(t, f.DTStart.Equal(want), "got %v", f.DTStart)
			assert.Equal(t, "America/Denver", f.DTStart.Location().String())
		})
	}
}

func TestParseVEventVTIMEZONEAlias(t *testing.T) {
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VTIMEZONE\r\nTZID:Custom Mountain\r\nX-LIC-LOCATION:America/Denver\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:a\r\nDTSTART;TZID=Custom Mountain:20260115T090000\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	f, err := ParseVEvent(ics)
	require.NoError(t, err)
	// January: MST, UTC-7.
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 16, 0, 0, 0, time.UTC)), "got %v", f.DTStart)
}

func TestParseVEventFloatingUsesCalendarZone(t *testing.T) {
	ics := vevent("DTSTART:20260115T090000", "DTEND:20260115T100000")

	f, err := ParseVEvent(ics)
	require.NoError(t, err)
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)), "default is UTC, got %v", f.DTStart)

	f, err = ParseVEventIn(ics, LoadTimezone("America/Denver"))
	require.NoError(t, err)
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 16, 0, 0, 0, time.UTC)), "got %v", f.DTStart)

	// An unknown TZID falls back to the calendar's zone too.
	f, err = ParseVEventIn(vevent("DTSTART;TZID=Nowhere/Special:20260115T090000"), LoadTimezone("America/Denver"))
	require.NoError(t, err)
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 16, 0, 0, 0, time.UTC)), "got %v", f.DTStart)
}

func TestParseVEventAllDayAndUTCUnchanged(t *testing.T) {
	f, err := ParseVEventIn(vevent("DTSTART;VALUE=DATE:20260115", "DTEND;VALUE=DATE:20260116"), LoadTimezone("America/Denver"))
	require.NoError(t, err)
	assert.True(t, f.IsAllDay)
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)), "all-day stays at UTC midnight, got %v", f.DTStart)

	f, err = ParseVEventIn(vevent("DTSTART:20260115T090000Z"), LoadTimezone("America/Denver"))
	require.NoError(t, err)
	assert.True(t, f.DTStart.Equal(time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)), "got %v", f.DTStart)
}

func TestGenerateICSKeepsZone(t *testing.T) {
	denver := LoadTimezone("America/Denver")
	start := time.Date(2026, 3, 2, 9, 0, 0, 0, denver)
	ics := GenerateICSFull("g", "Standup", "", "", start, start.Add(15*time.Minute), false, "FREQ=WEEKLY", nil, nil, start)
	assert.True(t, strings.Contains(ics, "DTSTART;TZID=America/Denver:20260302T090000\r\n"), ics)

	f, err := ParseVEvent(ics)
	require.NoError(t, err)
	assert.True(t, f.DTStart.Equal(start))

	// UTC times keep the Z form.
	ics = GenerateICSFull("g", "x", "", "", start.UTC(), start.UTC(), false, "", nil, nil, start)
	assert.True(t, strings.Contains(ics, "DTSTART:20260302T160000Z\r\n"), ics)
}

func TestExpandRRuleKeepsLocalTimeAcrossDST(t *testing.T) {
	// Weekly at 09:00 Denver, spanning the 2026-03-08 DST change.
	ics := vevent(
		"DTSTART;TZID=America/Denver:20260302T090000",
		"DTEND;TZID=America/Denver:20260302T100000",
		"RRULE:FREQ=WEEKLY;COUNT=3",
		"EXDATE;TZID=America/Denver:20260309T090000",
	)
	f, err := ParseVEvent(ics)
	require.NoError(t, err)
	ev := EventWithCalendar{Event: Event{DTStart: f.DTStart.Unix(), DTEnd: f.DTEnd.Unix(), ICSData: ics}}

	occ := expandRRule(ev, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC))
	denver := LoadTimezone("America/Denver")
	var got []string
	for _, o := range occ {
		got = append(got, time.Unix(o.DTStart, 0).In(denver).Format("Jan 2 15:04"))
	}
	// Mar 9 is excluded; Mar 16 is after DST and still 09:00 local.
	assert.Equal(t, "Mar 2 09:00,Mar 16 09:00", strings.Join(got, ","))
}

func TestTimezoneFromDAV(t *testing.T) {
	assert.Equal(t, "America/Denver", timezoneFromDAV(`<C:calendar-timezone-id>America/Denver</C:calendar-timezone-id>`))
	vtz := "<C:calendar-timezone>BEGIN:VCALENDAR&#13;\nBEGIN:VTIMEZONE&#13;\nTZID:America/Chicago&#13;\nEND:VTIMEZONE&#13;\nEND:VCALENDAR</C:calendar-timezone>"
	assert.Equal(t, "America/Chicago", timezoneFromDAV(vtz))
	assert.Equal(t, "", timezoneFromDAV(`<D:displayname>x</D:displayname>`))
}
