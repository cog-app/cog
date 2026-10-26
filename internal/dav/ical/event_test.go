package ical

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/tz"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func decode(t *testing.T, lines ...string) *Object {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))
	require.NoError(t, err)
	o, err := Decode(comps[0], VEVENT)
	require.NoError(t, err)
	return o
}

func cal(lines ...string) []string {
	return append(append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Test//EN"}, lines...), "END:VCALENDAR")
}

var zones = Zones{Floating: time.UTC, Load: tz.Load}

func TestTextEscaping(t *testing.T) {
	for _, s := range []string{"plain", "a, b; c\\d\nline", ""} {
		assert.Equal(t, s, UnescapeText(EscapeText(s)))
	}
	assert.Equal(t, "Room 1, Floor 2", UnescapeText("Room 1, Floor 2"))
	assert.Equal(t, `a\x`, UnescapeText(`a\x`))
	assert.Equal(t, "a\nb", UnescapeText(`a\Nb`))
}

func TestDuration(t *testing.T) {
	for in, want := range map[string]string{"-PT15M": "-PT15M", "P1D": "P1D", "P2W": "P2W", "PT1H30M": "PT1H30M", "P1DT2H": "P1DT2H", "PT0S": "PT0S"} {
		d, err := ParseDuration(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, d.String())
	}
	for _, bad := range []string{"", "P", "15M", "PT", "P1H", "PT1D"} {
		_, err := ParseDuration(bad)
		assert.True(t, err != nil, bad)
	}
	// A day is a calendar day: across the spring change it is 23 hours.
	ny := tz.Load("America/New_York")
	d, _ := ParseDuration("P1D")
	start := time.Date(2026, 3, 7, 9, 0, 0, 0, ny)
	assert.Equal(t, time.Date(2026, 3, 8, 9, 0, 0, 0, ny), d.Add(start))
}

func TestDecodeModelsAndLeftovers(t *testing.T) {
	o := decode(t, cal(
		"X-WR-CALNAME:Work",
		"BEGIN:VEVENT",
		"UID:a@example.com",
		"DTSTAMP:20261001T120000Z",
		"SUMMARY;LANGUAGE=de:Besprechung\\, wichtig",
		"DTSTART;TZID=Eastern Standard Time:20261025T090000",
		"DURATION:PT1H",
		"ATTENDEE;CN=Bob;PARTSTAT=ACCEPTED;X-NUM-GUESTS=2:mailto:bob@example.com",
		"ORGANIZER;CN=Alice:mailto:alice@example.com",
		"CATEGORIES:Work,Important",
		"SEQUENCE:0",
		"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC",
		"GEO:37.386013;-122.082932",
		"BEGIN:VALARM",
		"ACTION:DISPLAY",
		"TRIGGER;RELATED=END:-PT15M",
		"X-WR-ALARMUID:abc",
		"END:VALARM",
		"END:VEVENT",
	)...)
	require.Equal(t, 1, len(o.Components))
	e := o.Components[0]
	assert.Equal(t, "a@example.com", o.UID)
	assert.Equal(t, "Besprechung, wichtig", e.Summary)
	assert.Equal(t, "LANGUAGE", e.Leftovers.Params["SUMMARY"][0].Name)
	// A Windows zone name is stored as the IANA zone it names.
	assert.Equal(t, DateTime{Local: "20261025T090000", TZID: "America/New_York"}, e.Start)
	assert.Equal(t, 0, len(o.Zones))
	assert.Equal(t, "bob@example.com", Email(e.Attendees[0].Address))
	assert.Equal(t, "ACCEPTED", e.Attendees[0].PartStat)
	assert.Equal(t, "X-NUM-GUESTS", e.Attendees[0].Params[0].Name)
	assert.Equal(t, "Alice", e.Organizer.CN)
	assert.Equal(t, []string{"Work", "Important"}, e.Categories)
	assert.Equal(t, 0, *e.Sequence)
	assert.Equal(t, 2, len(e.Leftovers.Props))
	assert.Equal(t, "END", e.Alarms[0].Related)
	assert.Equal(t, "X-WR-ALARMUID", e.Alarms[0].Leftovers.Props[0].Name)

	start, end, err := e.Span(zones)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 25, 13, 0, 0, 0, time.UTC), start.UTC())
	assert.Equal(t, time.Hour, end.Sub(start))

	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{o.Encode(o.IANAZones)}))
	out := b.String()
	for _, want := range []string{
		"PRODID:-//Test//EN\r\nX-WR-CALNAME:Work\r\nBEGIN:VTIMEZONE\r\nTZID:America/New_York\r\n",
		"SUMMARY;LANGUAGE=de:Besprechung\\, wichtig\r\n",
		"DTSTART;TZID=America/New_York:20261025T090000\r\n",
		"PARTSTAT=ACCEPTED;CN=Bob;X-NUM-GUESTS=2:mailto:bob@example.com\r\n",
		"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\nGEO:37.386013;-122.082932\r\nBEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER;RELATED=END:-PT15M\r\nX-WR-ALARMUID:abc\r\n",
	} {
		assert.True(t, strings.Contains(out, want), "missing %q in\n%s", want, out)
	}
}

// TestCustomZoneKept keeps a TZID Cog cannot resolve with its VTIMEZONE, and
// reads times through that VTIMEZONE's rules.
func TestCustomZoneKept(t *testing.T) {
	o := decode(t, cal(
		"BEGIN:VTIMEZONE",
		"TZID:My Office",
		"BEGIN:STANDARD",
		"DTSTART:19700101T000000",
		"TZOFFSETFROM:+0300",
		"TZOFFSETTO:+0300",
		"END:STANDARD",
		"END:VTIMEZONE",
		"BEGIN:VTIMEZONE",
		"TZID:Unused",
		"BEGIN:STANDARD",
		"DTSTART:19700101T000000",
		"TZOFFSETFROM:+0100",
		"TZOFFSETTO:+0100",
		"END:STANDARD",
		"END:VTIMEZONE",
		"BEGIN:VEVENT",
		"UID:x",
		"DTSTART;TZID=My Office:20261025T090000",
		"END:VEVENT",
	)...)
	assert.Equal(t, "My Office", o.Components[0].Start.TZID)
	require.Equal(t, 1, len(o.Zones))
	loc, err := tz.FromRules(o.Zones["My Office"])
	require.NoError(t, err)
	z := zones
	z.Custom = map[string]*time.Location{"My Office": loc}
	start, _, err := o.Components[0].Span(z)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 25, 6, 0, 0, 0, time.UTC), start.UTC())
}

func TestChangedDropsDependents(t *testing.T) {
	o := decode(t, cal(
		"BEGIN:VEVENT", "UID:x", "DTSTART:20261025T090000Z",
		"DESCRIPTION:Plain", "X-ALT-DESC;FMTTYPE=text/html:<p>Plain</p>",
		"LOCATION:Office", "X-APPLE-STRUCTURED-LOCATION;X-TITLE=Office:geo:1,2",
		"X-MOZ-GENERATION:3", "X-MOZ-SNOOZE-TIME-1234:20261025T083000Z", "X-KEEP:me",
		"END:VEVENT",
	)...)
	e := o.Components[0]
	e.Changed("SUMMARY")
	assert.Equal(t, 5, len(e.Leftovers.Props))
	e.Changed("LOCATION", "TIME")
	names := []string{}
	for _, p := range e.Leftovers.Props {
		names = append(names, p.Name)
	}
	assert.Equal(t, []string{"X-ALT-DESC", "X-KEEP"}, names)
	e.Changed("DESCRIPTION")
	assert.Equal(t, 1, len(e.Leftovers.Props))
}

func instants(t *testing.T, o *Object, from, to string) []string {
	t.Helper()
	f, _ := time.Parse(time.RFC3339, from)
	u, _ := time.Parse(time.RFC3339, to)
	ins, err := o.Instances(zones, f, u)
	require.NoError(t, err)
	var out []string
	for _, in := range ins {
		out = append(out, in.Start.UTC().Format("01-02T15")+" "+in.Event.Summary)
	}
	return out
}

func TestInstances(t *testing.T) {
	o := decode(t, cal(
		"BEGIN:VEVENT", "UID:r", "SUMMARY:Standup",
		"DTSTART;TZID=Europe/Berlin:20261020T090000", "DTEND;TZID=Europe/Berlin:20261020T091500",
		"RRULE:FREQ=DAILY;COUNT=10",
		"EXDATE;TZID=Europe/Berlin:20261022T090000",
		"EXDATE:20261023T070000Z",
		"END:VEVENT",
		// Moved to the afternoon.
		"BEGIN:VEVENT", "UID:r", "SUMMARY:Moved", "RECURRENCE-ID;TZID=Europe/Berlin:20261021T090000",
		"DTSTART;TZID=Europe/Berlin:20261021T150000", "DTEND;TZID=Europe/Berlin:20261021T151500",
		"END:VEVENT",
		// An override of an excluded occurrence: the EXDATE wins.
		"BEGIN:VEVENT", "UID:r", "SUMMARY:Ghost", "RECURRENCE-ID;TZID=Europe/Berlin:20261022T090000",
		"DTSTART;TZID=Europe/Berlin:20261022T100000", "END:VEVENT",
		// From the 27th on, an hour later and renamed.
		"BEGIN:VEVENT", "UID:r", "SUMMARY:Later", "RECURRENCE-ID;RANGE=THISANDFUTURE;TZID=Europe/Berlin:20261027T090000",
		"DTSTART;TZID=Europe/Berlin:20261027T100000", "DTEND;TZID=Europe/Berlin:20261027T101500",
		"END:VEVENT",
		// Orphan: no occurrence at this time; shown on its own.
		"BEGIN:VEVENT", "UID:r", "SUMMARY:Orphan", "RECURRENCE-ID;TZID=Europe/Berlin:20261024T120000",
		"DTSTART;TZID=Europe/Berlin:20261024T120000", "END:VEVENT",
	)...)
	assert.Equal(t, []string{
		"10-20T07 Standup", "10-21T13 Moved", "10-24T07 Standup", "10-24T10 Orphan",
		// The clocks went back on the 25th: 09:00 Berlin is 08:00 UTC.
		"10-25T08 Standup", "10-26T08 Standup",
		"10-27T09 Later", "10-28T09 Later", "10-29T09 Later",
	}, instants(t, o, "2026-10-01T00:00:00Z", "2026-11-30T00:00:00Z"))
	assert.Equal(t, []string{"10-21T13 Moved"}, instants(t, o, "2026-10-21T12:00:00Z", "2026-10-21T14:00:00Z"))

	start, end, unbounded, err := o.Bounds(zones)
	require.NoError(t, err)
	assert.False(t, unbounded)
	assert.Equal(t, "2026-10-20T07:00:00Z", start.UTC().Format(time.RFC3339))
	assert.Equal(t, "2026-10-29T09:15:00Z", end.UTC().Format(time.RFC3339))
}

func TestInstancesAllDayAndDates(t *testing.T) {
	o := decode(t, cal(
		"BEGIN:VEVENT", "UID:b", "SUMMARY:Birthday", "DTSTART;VALUE=DATE:19900315", "RRULE:FREQ=YEARLY",
		"END:VEVENT",
	)...)
	ins, err := o.Instances(zones, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, len(ins))
	assert.Equal(t, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), ins[0].Start)
	assert.Equal(t, time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC), ins[0].End)
	_, _, unbounded, err := o.Bounds(zones)
	require.NoError(t, err)
	assert.True(t, unbounded)

	// A DATE EXDATE on a timed event removes that day; RDATE adds one.
	o = decode(t, cal(
		"BEGIN:VEVENT", "UID:c", "SUMMARY:Class", "DTSTART:20261005T170000Z", "DURATION:PT1H",
		"RRULE:FREQ=WEEKLY;UNTIL=20261026T170000Z", "EXDATE;VALUE=DATE:20261012",
		"RDATE;VALUE=PERIOD:20261030T170000Z/PT2H",
		"END:VEVENT",
	)...)
	assert.Equal(t, []string{"10-05T17 Class", "10-19T17 Class", "10-26T17 Class", "10-30T17 Class"},
		instants(t, o, "2026-10-01T00:00:00Z", "2026-11-30T00:00:00Z"))

	// Floating times follow the collection's zone.
	o = decode(t, cal("BEGIN:VEVENT", "UID:f", "SUMMARY:F", "DTSTART:20261005T090000", "END:VEVENT")...)
	z := zones
	z.Floating = tz.Load("America/Denver")
	start, _, err := o.Components[0].Span(z)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC), start.UTC())
}

func TestBusy(t *testing.T) {
	o := decode(t, cal(
		"BEGIN:VEVENT", "UID:p", "SUMMARY:Doctor", "DESCRIPTION:Private", "LOCATION:Clinic",
		"DTSTART:20261005T170000Z", "DURATION:PT1H", "TRANSP:OPAQUE",
		"ATTENDEE:mailto:x@example.com", "X-SECRET:1",
		"END:VEVENT",
	)...)
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{o.Busy().Encode(nil)}))
	out := b.String()
	for _, gone := range []string{"Doctor", "Private", "Clinic", "x@example.com", "X-SECRET"} {
		assert.False(t, strings.Contains(out, gone), "%s leaked into\n%s", gone, out)
	}
	assert.True(t, strings.Contains(out, "SUMMARY:Busy\r\n"))
	assert.True(t, strings.Contains(out, "TRANSP:OPAQUE\r\n"))
}

func TestBadRuleIsOneOccurrence(t *testing.T) {
	o := decode(t, cal("BEGIN:VEVENT", "UID:x", "SUMMARY:X", "DTSTART:20261005T170000Z", "RRULE:FREQ=SOMETIMES", "END:VEVENT")...)
	assert.Equal(t, []string{"10-05T17 X"}, instants(t, o, "2026-10-01T00:00:00Z", "2026-11-30T00:00:00Z"))
	_, _, unbounded, err := o.Bounds(zones)
	require.NoError(t, err)
	assert.False(t, unbounded)
}

// TestDecodeTZIDForms covers the TZID spellings clients send. Each resolves
// to the IANA zone, which is what Cog stores and writes back.
func TestDecodeTZIDForms(t *testing.T) {
	// 09:00 MDT on 2026-10-25 is 15:00 UTC.
	want := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	for name, prop := range map[string]string{
		"iana":            "DTSTART;TZID=America/Denver:20261025T090000",
		"quoted":          `DTSTART;TZID="America/Denver":20261025T090000`,
		"windows":         "DTSTART;TZID=Mountain Standard Time:20261025T090000",
		"mozilla prefix":  "DTSTART;TZID=/mozilla.org/20050126_1/America/Denver:20261025T090000",
		"lowercase param": "DTSTART;tzid=America/Denver:20261025T090000",
	} {
		t.Run(name, func(t *testing.T) {
			o := decode(t, cal("BEGIN:VEVENT", "UID:x", prop, "END:VEVENT")...)
			assert.Equal(t, "America/Denver", o.Components[0].Start.TZID)
			start, _, err := o.Components[0].Span(zones)
			require.NoError(t, err)
			assert.Equal(t, want, start.UTC())
		})
	}

	// Thunderbird names its zone and points at IANA with X-LIC-LOCATION.
	o := decode(t, cal("BEGIN:VTIMEZONE", "TZID:Custom Mountain", "X-LIC-LOCATION:America/Denver", "END:VTIMEZONE",
		"BEGIN:VEVENT", "UID:a", "DTSTART;TZID=Custom Mountain:20261025T090000", "END:VEVENT")...)
	assert.Equal(t, "America/Denver", o.Components[0].Start.TZID)
	assert.Equal(t, 0, len(o.Zones))

	// A TZID with no definition stays as sent and reads in the floating zone.
	o = decode(t, cal("BEGIN:VEVENT", "UID:a", "DTSTART;TZID=Nowhere/Special:20261025T090000", "END:VEVENT")...)
	assert.Equal(t, "Nowhere/Special", o.Components[0].Start.TZID)
	z := zones
	z.Floating = tz.Load("America/Denver")
	start, _, err := o.Components[0].Span(z)
	require.NoError(t, err)
	assert.Equal(t, want, start.UTC())
}

func TestInstancesKeepLocalTimeAcrossDST(t *testing.T) {
	// Weekly at 09:00 Denver, spanning the 2026-03-08 change.
	o := decode(t, cal("BEGIN:VEVENT", "UID:w", "SUMMARY:W",
		"DTSTART;TZID=America/Denver:20260302T090000", "DTEND;TZID=America/Denver:20260302T100000",
		"RRULE:FREQ=WEEKLY;COUNT=3", "EXDATE;TZID=America/Denver:20260309T090000", "END:VEVENT")...)
	// Mar 9 is excluded; Mar 16 is after the change and still 09:00 local
	// (15:00 UTC, was 16:00).
	assert.Equal(t, []string{"03-02T16 W", "03-16T15 W"}, instants(t, o, "2026-03-01T00:00:00Z", "2026-04-01T00:00:00Z"))
}
