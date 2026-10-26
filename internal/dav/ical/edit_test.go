package ical

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func weekly(t *testing.T, rule string) *Object {
	t.Helper()
	return decode(t, cal(
		"BEGIN:VEVENT", "UID:w", "SUMMARY:Standup", "SEQUENCE:1",
		"DTSTART;TZID=Europe/Berlin:20261005T090000", "DTEND;TZID=Europe/Berlin:20261005T091500",
		"RRULE:"+rule, "EXDATE;TZID=Europe/Berlin:20261026T090000", "X-KEEP:1",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT5M", "END:VALARM",
		"END:VEVENT",
		"BEGIN:VEVENT", "UID:w", "SUMMARY:Moved", "RECURRENCE-ID;TZID=Europe/Berlin:20261019T090000",
		"DTSTART;TZID=Europe/Berlin:20261019T100000", "DTEND;TZID=Europe/Berlin:20261019T101500", "END:VEVENT",
	)...)
}

func berlin(day int) time.Time {
	return time.Date(2026, 10, day, 9, 0, 0, 0, zones.Location("Europe/Berlin"))
}

func text(t *testing.T, o *Object) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{o.Encode(nil)}))
	return b.String()
}

func TestOverrideCopiesMaster(t *testing.T) {
	o := weekly(t, "FREQ=WEEKLY;COUNT=6")
	_, err := o.Override(berlin(13), zones)
	assert.Equal(t, ErrNoOccurrence, err, "a Tuesday is no occurrence")

	e, err := o.Override(berlin(12), zones)
	require.NoError(t, err)
	assert.Equal(t, "Standup", e.Summary)
	assert.Equal(t, DateTime{Local: "20261012T090000", TZID: "Europe/Berlin"}, e.RecurrenceID)
	assert.Equal(t, DateTime{Local: "20261012T091500", TZID: "Europe/Berlin"}, e.End)
	assert.Equal(t, "", e.RRule)
	assert.Equal(t, 0, len(e.ExDates))
	assert.Equal(t, 1, len(e.Alarms))
	assert.True(t, e.Leftovers.Has("X-KEEP"))
	e.Summary = "Just this once"
	assert.Equal(t, "Standup", o.Master().Summary, "the copy shares nothing")

	// The existing override is returned, not a second one.
	again, err := o.Override(berlin(19), zones)
	require.NoError(t, err)
	assert.Equal(t, "Moved", again.Summary)
	assert.Equal(t, 3, len(o.Components))
}

func TestExclude(t *testing.T) {
	o := weekly(t, "FREQ=WEEKLY;COUNT=6")
	assert.True(t, o.Exclude(berlin(19), zones))
	assert.Equal(t, 1, len(o.Components), "the override went with its occurrence")
	out := text(t, o)
	assert.True(t, strings.Contains(out, "EXDATE;TZID=Europe/Berlin:20261026T090000,20261019T090000\r\n"), out)
	assert.Equal(t, []string{"10-05T07 Standup", "10-12T07 Standup", "11-02T08 Standup", "11-09T08 Standup"},
		instants(t, o, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z"))
}

func TestTruncate(t *testing.T) {
	o := weekly(t, "FREQ=WEEKLY;COUNT=6")
	left, err := o.Truncate(berlin(19), zones)
	require.NoError(t, err)
	assert.True(t, left)
	assert.Equal(t, "FREQ=WEEKLY;UNTIL=20261019T065959Z", o.Master().RRule)
	assert.Equal(t, 0, len(o.Master().ExDates))
	assert.Equal(t, 1, len(o.Components))
	assert.Equal(t, []string{"10-05T07 Standup", "10-12T07 Standup"}, instants(t, o, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z"))

	left, err = o.Truncate(berlin(5), zones)
	require.NoError(t, err)
	assert.False(t, left, "nothing is left before the first occurrence")
}

func TestSplit(t *testing.T) {
	o := weekly(t, "FREQ=WEEKLY;COUNT=6")
	n, err := o.Split(berlin(19), zones, "w2")
	require.NoError(t, err)
	assert.Equal(t, []string{"10-05T07 Standup", "10-12T07 Standup"}, instants(t, o, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z"))
	assert.Equal(t, "w2", n.UID)
	assert.Equal(t, "FREQ=WEEKLY;COUNT=4", n.Master().RRule)
	// The override and the exception moved with their occurrences; COUNT
	// counts what was left, the exception included.
	assert.Equal(t, []string{"10-19T08 Moved", "11-02T08 Standup", "11-09T08 Standup"}, instants(t, n, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z"))

	// An UNTIL stays as it was.
	o = weekly(t, "FREQ=WEEKLY;UNTIL=20261231T000000Z")
	n, err = o.Split(berlin(12), zones, "w3")
	require.NoError(t, err)
	assert.Equal(t, "FREQ=WEEKLY;UNTIL=20261231T000000Z", n.Master().RRule)

	_, err = decode(t, cal("BEGIN:VEVENT", "UID:s", "DTSTART:20261005T090000Z", "END:VEVENT")...).Split(berlin(5), zones, "x")
	assert.Equal(t, ErrNotRecurring, err)
}

func TestShiftExceptions(t *testing.T) {
	o := weekly(t, "FREQ=WEEKLY;COUNT=6")
	m := o.Master()
	m.Start = shifted(m.Start, time.Hour, zones)
	m.End = shifted(m.End, time.Hour, zones)
	o.ShiftExceptions(time.Hour, zones)
	assert.Equal(t, DateTime{Local: "20261026T100000", TZID: "Europe/Berlin"}, m.ExDates[0].DateTime)
	assert.Equal(t, DateTime{Local: "20261019T100000", TZID: "Europe/Berlin"}, o.Components[1].RecurrenceID)
	got := instants(t, o, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z")
	assert.Equal(t, []string{"10-05T08 Standup", "10-12T08 Standup", "10-19T08 Moved", "11-02T09 Standup", "11-09T09 Standup"}, got)
}

func TestAllDayUntil(t *testing.T) {
	o := decode(t, cal("BEGIN:VEVENT", "UID:a", "SUMMARY:A", "DTSTART;VALUE=DATE:20261005", "RRULE:FREQ=DAILY", "END:VEVENT")...)
	_, err := o.Truncate(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC), zones)
	require.NoError(t, err)
	assert.Equal(t, "FREQ=DAILY;UNTIL=20261007", o.Master().RRule)
	assert.Equal(t, 3, len(instants(t, o, "2026-10-01T00:00:00Z", "2026-12-01T00:00:00Z")))
}
