package ical

import (
	"bytes"
	"os"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func fixture(t *testing.T, name string) *Object {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/dav/ics/" + name)
	require.NoError(t, err)
	comps, err := contentline.Parse(bytes.NewReader(data))
	require.NoError(t, err)
	o, err := Decode(comps[0], VEVENT)
	require.NoError(t, err)
	return o
}

func size(n int64) *int64 { return &n }

// TestAttachVariants decodes ATTACH in each form the corpus has.
func TestAttachVariants(t *testing.T) {
	// RFC 8607: a managed attachment, and an override's own.
	o := fixture(t, "rfc8607/A-add-instance.ics")
	require.Equal(t, 2, len(o.Components))
	assert.Equal(t, []Attachment{{Value: "https://cal.example.com/attach/65/34X22R", ManagedID: "97S", FmtType: "text/html", Size: size(80), Filename: "agenda.html"}}, o.Components[0].Attachments)
	assert.Equal(t, "33225", o.Components[1].Attachments[0].ManagedID)
	assert.Equal(t, []string{"97S", "33225"}, o.ManagedIDs())
	assert.Equal(t, 0, len(o.Components[0].Leftovers.Props))

	// CalendarServer's managed form.
	o = fixture(t, "calendarserver/managed-attachment.ics")
	a := o.Components[0].Attachments[0]
	assert.Equal(t, "a1b2c3d4-0000-4000-8000-000000000001", a.ManagedID)
	assert.Equal(t, "new.attachment", a.Filename)
	assert.Equal(t, "text/x-fixture", a.FmtType)

	// Apple's dropbox: a relative URI, VALUE=URI kept as written.
	o = fixture(t, "calendarserver/dropbox-attachment.ics")
	a = o.Components[0].Attachments[0]
	assert.Equal(t, "/calendars/__uids__/user01/dropbox/uid1.dropbox/test.txt", a.Value)
	assert.Equal(t, "VALUE", a.Params[0].Name)
	assert.False(t, a.Inline)

	// Inline base64.
	o = fixture(t, "python-icalendar/issue_1549_binary_attachment.ics")
	a = o.Components[0].Attachments[0]
	assert.True(t, a.Inline)
	assert.Equal(t, "image/png", a.FmtType)

	// What does not fit stays a leftover: no value, a size that is not a
	// number, inline data in another encoding, a managed ID on inline data.
	o = decode(t, cal("BEGIN:VEVENT", "UID:x",
		"ATTACH;SIZE=big:https://example.com/a",
		"ATTACH;VALUE=BINARY;ENCODING=8BIT:abc",
		"ATTACH;VALUE=BINARY;ENCODING=BASE64;MANAGED-ID=1:YWJj",
		"ATTACH:",
		"END:VEVENT")...)
	assert.Equal(t, 0, len(o.Components[0].Attachments))
	assert.Equal(t, 4, len(o.Components[0].Leftovers.Props))
}

// TestAttachmentTargets checks how RFC 8607's rid parameter names
// components, creating an override for an occurrence that has none.
func TestAttachmentTargets(t *testing.T) {
	o := fixture(t, "rfc8607/A-add-recurring.ics")
	z := ZonesFor(o, time.UTC)

	all, err := o.Targets(nil, z)
	require.NoError(t, err)
	assert.Equal(t, 1, len(all))

	got, err := o.Targets([]string{"M", "20120220T100000"}, z)
	require.NoError(t, err)
	require.Equal(t, 2, len(got))
	assert.Equal(t, 2, len(o.Components))
	assert.Equal(t, "20120220T100000", got[1].RecurrenceID.Local)
	assert.Equal(t, "America/Montreal", got[1].RecurrenceID.TZID)
	// The new override copies the master, attachments included, without
	// its recurrence.
	assert.Equal(t, "97S", got[1].Attachments[0].ManagedID)
	assert.Equal(t, "", got[1].RRule)

	// The same instance again finds the override.
	again, err := o.Targets([]string{"20120220T100000"}, z)
	require.NoError(t, err)
	assert.True(t, again[0] == got[1])

	for _, bad := range [][]string{{"20120221T100000"}, {"M", "m"}, {"garbage"}} {
		_, err := o.Targets(bad, z)
		assert.True(t, err != nil, bad)
	}

	assert.True(t, got[1].RemoveAttachment("97S"))
	assert.False(t, got[1].RemoveAttachment("97S"))
	assert.Equal(t, 1, len(o.Holding("97S")))
}
