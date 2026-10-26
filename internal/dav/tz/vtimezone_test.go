package tz

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestVTimezoneRoundTrip generates a VTIMEZONE from Go's zone data, reads it
// back with the rule evaluator a client's custom VTIMEZONE goes through, and
// checks the two agree on every hour from the start year to well past the
// scanned range. That covers both directions: what Cog sends clients, and
// what it computes from what they send.
func TestVTimezoneRoundTrip(t *testing.T) {
	zones := []string{
		"America/New_York", "America/Denver", "America/Phoenix", "Europe/London",
		"Europe/Berlin", "Australia/Sydney", "Pacific/Auckland", "Asia/Kolkata",
		"America/Sao_Paulo", "Africa/Casablanca", "Asia/Tehran", "Pacific/Apia",
		"America/St_Johns", "Australia/Lord_Howe", "Europe/Moscow", "UTC",
	}
	for _, name := range zones {
		t.Run(name, func(t *testing.T) {
			loc := Load(name)
			require.True(t, loc != nil, "zone %s", name)
			for _, from := range []int{1995, 2015, 2026} {
				vtz := VTimezone(loc, from)
				got, err := FromRules(vtz)
				require.NoError(t, err)
				end := time.Date(2045, 1, 1, 0, 0, 0, 0, time.UTC)
				var bad int
				for at := time.Date(from, 1, 1, 12, 0, 0, 0, time.UTC); at.Before(end); at = at.Add(time.Hour) {
					_, want := at.In(loc).Zone()
					_, have := at.In(got).Zone()
					if want != have {
						if bad == 0 {
							var b bytes.Buffer
							require.NoError(t, contentline.Write(&b, []*contentline.Component{vtz}))
							t.Errorf("from %d at %s: offset %d, want %d\n%s", from, at, have, want, b.String())
						}
						bad++
					}
				}
				assert.Equal(t, 0, bad)
			}
		})
	}
}

func TestVTimezoneShape(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{VTimezone(Load("America/New_York"), 2026)}))
	assert.Equal(t, strings.Join([]string{
		"BEGIN:VTIMEZONE",
		"TZID:America/New_York",
		"BEGIN:DAYLIGHT",
		"TZOFFSETFROM:-0500",
		"TZOFFSETTO:-0400",
		"TZNAME:EDT",
		"DTSTART:20260308T020000",
		"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU",
		"END:DAYLIGHT",
		"BEGIN:STANDARD",
		"TZOFFSETFROM:-0400",
		"TZOFFSETTO:-0500",
		"TZNAME:EST",
		"DTSTART:20261101T020000",
		"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU",
		"END:STANDARD",
		"END:VTIMEZONE",
		"",
	}, "\r\n"), b.String())

	// No changes: one STANDARD.
	b.Reset()
	require.NoError(t, contentline.Write(&b, []*contentline.Component{VTimezone(Load("Asia/Kolkata"), 2026)}))
	assert.True(t, strings.Contains(b.String(), "TZOFFSETFROM:+0530\r\nTZOFFSETTO:+0530\r\nTZNAME:IST\r\nDTSTART:20260101T000000\r\n"), b.String())
}

// TestFromRulesClientVTimezone reads the kind of VTIMEZONE Outlook sends: a
// Windows name, with rules and no X-LIC-LOCATION.
func TestFromRulesClientVTimezone(t *testing.T) {
	comps, err := contentline.Parse(strings.NewReader(strings.Join([]string{
		"BEGIN:VTIMEZONE",
		"TZID:Custom Standard Time",
		"BEGIN:STANDARD",
		"DTSTART:16010101T030000",
		"TZOFFSETFROM:+0200",
		"TZOFFSETTO:+0100",
		"RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=-1SU;BYMONTH=10",
		"END:STANDARD",
		"BEGIN:DAYLIGHT",
		"DTSTART:16010101T020000",
		"TZOFFSETFROM:+0100",
		"TZOFFSETTO:+0200",
		"RRULE:FREQ=YEARLY;INTERVAL=1;BYDAY=-1SU;BYMONTH=3",
		"END:DAYLIGHT",
		"END:VTIMEZONE",
	}, "\r\n")))
	require.NoError(t, err)
	loc, err := FromRules(comps[0])
	require.NoError(t, err)
	assert.Equal(t, "Custom Standard Time", loc.String())
	berlin := Load("Europe/Berlin")
	for _, d := range []string{"2026-01-15", "2026-03-29T00:30:00Z", "2026-03-29T01:30:00Z", "2026-07-01", "2026-10-25T00:30:00Z", "2026-10-25T01:30:00Z"} {
		at, err := time.Parse(time.RFC3339, d)
		if err != nil {
			at, err = time.Parse("2006-01-02", d)
		}
		require.NoError(t, err)
		_, want := at.In(berlin).Zone()
		_, have := at.In(loc).Zone()
		assert.Equal(t, want, have, d)
	}
}

func TestFromRulesRejects(t *testing.T) {
	comps, err := contentline.Parse(strings.NewReader("BEGIN:VTIMEZONE\r\nTZID:X\r\nBEGIN:STANDARD\r\nTZOFFSETFROM:nope\r\nTZOFFSETTO:+0100\r\nDTSTART:20000101T000000\r\nEND:STANDARD\r\nEND:VTIMEZONE\r\n"))
	require.NoError(t, err)
	_, err = FromRules(comps[0])
	assert.True(t, err != nil)
}
