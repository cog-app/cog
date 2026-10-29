package ical

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func decodeKind(t *testing.T, kind Kind, lines ...string) *Object {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(strings.Join(lines, "\r\n") + "\r\n"))
	require.NoError(t, err)
	o, err := Decode(comps[0], kind)
	require.NoError(t, err)
	return o
}

func TestDecodeTodo(t *testing.T) {
	o := decodeKind(t, VTODO, cal(
		"BEGIN:VTODO", "UID:t", "SUMMARY:Task", "DUE;VALUE=DATE:20261030", "COMPLETED:20261029T100000Z",
		"PERCENT-COMPLETE:40", "X-APPLE-SORT-ORDER:430306542",
		"RELATED-TO;RELTYPE=SIBLING:s", "RELATED-TO;X-A=1:p", "RELATED-TO;RELTYPE=PARENT:q",
		"DTEND:20261030T100000Z", "TRANSP:OPAQUE",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT5M", "END:VALARM",
		"END:VTODO",
	)...)
	e := o.Components[0]
	assert.Equal(t, VTODO, e.Kind)
	assert.Equal(t, DateTime{Local: "20261030", IsDate: true}, e.Due)
	assert.Equal(t, DateTime{Local: "20261029T100000", TZID: "UTC"}, e.Completed)
	assert.Equal(t, 40, *e.PercentComplete)
	assert.Equal(t, 430306542, *e.SortOrder)
	assert.Equal(t, 3, len(e.Relations))
	// The first PARENT relation is the parent; RELTYPE absent means PARENT.
	assert.Equal(t, "p", e.Parent())
	assert.Equal(t, 1, len(e.Alarms))
	// DTEND and TRANSP are not VTODO properties: they stay as received.
	assert.True(t, e.Leftovers.Has("DTEND") && e.Leftovers.Has("TRANSP"))

	out := text(t, o)
	for _, want := range []string{"RELATED-TO;RELTYPE=SIBLING:s", "RELATED-TO;X-A=1:p", "RELATED-TO;RELTYPE=PARENT:q", "X-APPLE-SORT-ORDER:430306542", "DTEND:20261030T100000Z"} {
		assert.Contains(t, out, want)
	}

	// Changing the parent keeps the relation's parameters; clearing it
	// removes every parent relation and keeps the others.
	assert.True(t, e.SetParent("r"))
	assert.False(t, e.SetParent("r"))
	assert.Contains(t, text(t, o), "RELATED-TO;X-A=1:r")
	assert.True(t, e.SetParent(""))
	assert.Equal(t, 1, len(e.Relations))
	assert.Equal(t, "", e.Parent())
}

func TestDecodeJournal(t *testing.T) {
	o := decodeKind(t, VJOURNAL, cal(
		"BEGIN:VJOURNAL", "UID:j", "SUMMARY:Day", "DESCRIPTION:One", "DESCRIPTION:Two", "LOCATION:Here",
		"BEGIN:VALARM", "ACTION:DISPLAY", "TRIGGER:-PT5M", "END:VALARM",
		"END:VJOURNAL",
		"BEGIN:VTODO", "UID:j", "END:VTODO",
	)...)
	e := o.Components[0]
	assert.Equal(t, "One", e.Description)
	// A second DESCRIPTION, a LOCATION and a VALARM are not modelled in a
	// journal; another kind of component is kept beside it.
	assert.True(t, e.Leftovers.Has("DESCRIPTION") && e.Leftovers.Has("LOCATION"))
	assert.Equal(t, 0, len(e.Alarms))
	assert.Equal(t, 1, len(e.Leftovers.Comps))
	assert.Equal(t, 1, len(o.Others))
	out := text(t, o)
	assert.True(t, strings.Index(out, "DESCRIPTION:One") < strings.Index(out, "DESCRIPTION:Two"))
}

func TestTodoRecurrence(t *testing.T) {
	// A task recurring by its due date alone: occurrences are instants at
	// DUE.
	o := decodeKind(t, VTODO, cal("BEGIN:VTODO", "UID:d", "DUE:20261001T090000Z", "RRULE:FREQ=DAILY;COUNT=3", "END:VTODO")...)
	ins, err := o.Instances(zones, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 3, len(ins))
	assert.Equal(t, ins[1].Start, ins[1].End)
	assert.Equal(t, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), ins[1].Start)

	ok, err := o.Advance(zones)
	require.NoError(t, err)
	require.True(t, ok)
	m := o.Components[0]
	assert.Equal(t, "20261002T090000", m.Due.Local)
	assert.Equal(t, "FREQ=DAILY;COUNT=2", m.RRule)

	// With DTSTART and DUE, each occurrence lasts as long as the first.
	o = decodeKind(t, VTODO, cal("BEGIN:VTODO", "UID:s", "DTSTART:20261001T090000Z", "DUE:20261001T110000Z",
		"RRULE:FREQ=WEEKLY", "END:VTODO")...)
	ok, err = o.Advance(zones)
	require.NoError(t, err)
	require.True(t, ok)
	m = o.Components[0]
	assert.Equal(t, "20261008T090000", m.Start.Local)
	assert.Equal(t, "20261008T110000", m.Due.Local)
	assert.Equal(t, "FREQ=WEEKLY", m.RRule)

	// The last occurrence has nothing to advance to.
	o = decodeKind(t, VTODO, cal("BEGIN:VTODO", "UID:l", "DUE:20261001T090000Z", "RRULE:FREQ=DAILY;COUNT=1", "END:VTODO")...)
	ok, err = o.Advance(zones)
	require.NoError(t, err)
	assert.False(t, ok)
}
