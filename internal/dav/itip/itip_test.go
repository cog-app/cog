package itip

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/contentline"
	"git.duckfam.us/jj-software/cog/internal/dav/ical"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

const corpus = "../../../testdata/dav/ics"

func load(t *testing.T, name string) *ical.Object {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpus, name))
	require.NoError(t, err)
	return parse(t, string(data))
}

func parse(t *testing.T, text string) *ical.Object {
	t.Helper()
	comps, err := contentline.Parse(strings.NewReader(text))
	require.NoError(t, err)
	require.Equal(t, 1, len(comps))
	o, err := ical.Decode(comps[0], ical.VEVENT)
	require.NoError(t, err)
	return o
}

func write(t *testing.T, c *contentline.Component) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, contentline.Write(&b, []*contentline.Component{c}))
	return b.String()
}

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func is(addrs ...string) func(string) bool {
	return func(a string) bool { return slices.Contains(addrs, a) }
}

func partstat(t *testing.T, e *ical.Component, addr string) string {
	t.Helper()
	a := attendee(e, addr)
	require.True(t, a != nil, "no attendee "+addr)
	return a.PartStat
}

// starts lists an object's instance starts in [from, to), as UTC times,
// with their location.
func starts(t *testing.T, o *ical.Object, from, to time.Time) []string {
	t.Helper()
	ins, err := o.Instances(ical.ZonesFor(o, time.UTC), from, to)
	require.NoError(t, err)
	var out []string
	for _, in := range ins {
		s := in.Start.UTC().Format("0102T15")
		if in.Event.Location != "" {
			s += " " + in.Event.Location
		}
		out = append(out, s)
	}
	return out
}

// TestApplySeriesFromRFC5546 follows an attendee's copy of the series in
// RFC 5546 §4.4 through each message the organizer sends.
func TestApplySeriesFromRFC5546(t *testing.T) {
	from, to := time.Date(1997, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(1997, 12, 1, 0, 0, 0, 0, time.UTC)

	cur, changed := Apply(Request, nil, load(t, "rfc5546/4.4.2-request.ics"), time.UTC)
	require.True(t, changed)
	assert.False(t, slices.ContainsFunc(cur.Props, func(p contentline.Property) bool { return p.Is("METHOD") }), "METHOD stored")
	assert.Equal(t, []string{
		"0601T21 Conference Call", "0701T21 Conference Call", "0801T21 Conference Call",
		"0901T21 Conference Call", "1001T21 Conference Call", "1101T21 Conference Call",
	}, starts(t, cur, from, to))

	// An alarm the attendee added stays through the organizer's updates.
	cur.Master().Alarms = []ical.Alarm{{Action: "DISPLAY", Trigger: "-PT5M"}}

	cur, changed = Apply(Request, cur, load(t, "rfc5546/4.4.2-modify-instance.ics"), time.UTC)
	require.True(t, changed)
	assert.Equal(t, "0703T21 Conference Call", starts(t, cur, from, to)[1])
	assert.Equal(t, 1, len(cur.Components[1].Alarms))

	cur, changed = Apply(Cancel, cur, load(t, "rfc5546/4.4.3-cancel-instance.ics"), time.UTC)
	require.True(t, changed)
	assert.Equal(t, []string{
		"0601T21 Conference Call", "0703T21 Conference Call",
		"0901T21 Conference Call", "1001T21 Conference Call", "1101T21 Conference Call",
	}, starts(t, cur, from, to))

	cur, changed = Apply(Request, cur, load(t, "rfc5546/4.4.5-this-and-future-range.ics"), time.UTC)
	require.True(t, changed)
	seattle := "Building 32, Microsoft, Seattle, WA"
	assert.Equal(t, []string{
		"0601T21 Conference Call", "0703T21 Conference Call",
		"0901T21 " + seattle, "1001T21 " + seattle, "1101T21 " + seattle,
	}, starts(t, cur, from, to))
	assert.Equal(t, 1, len(cur.Master().Alarms))

	cur, changed = Apply(Cancel, cur, load(t, "rfc5546/4.4.4-cancel-series.ics"), time.UTC)
	require.True(t, changed)
	for _, e := range cur.Components {
		assert.Equal(t, "CANCELLED", e.Status)
	}
}

// TestApplyQuirks applies RFC 5546's own broken examples: whatever they get
// wrong, the message still means what it says where it can be read.
func TestApplyQuirks(t *testing.T) {
	stored := load(t, "rfc5546/4.2.3-update.ics")

	// An older REQUEST (SEQUENCE 0, and an unreadable DTEND) arriving after
	// the update changes nothing.
	got, changed := Apply(Request, stored, load(t, "quirks/rfc5546/4.2.1-request.ics"), time.UTC)
	assert.False(t, changed)
	assert.Equal(t, "Phone Conference", got.Master().Summary)

	// A CANCEL whose first attendee is unreadable still cancels the event.
	got, changed = Apply(Cancel, stored, load(t, "quirks/rfc5546/4.2.9-cancel.ics"), time.UTC)
	require.True(t, changed)
	assert.Equal(t, "CANCELLED", got.Master().Status)

	// A bare THISANDFUTURE parameter is not RANGE=THISANDFUTURE: the
	// override replaces its one occurrence, and the parameter is kept.
	series, _ := Apply(Request, nil, load(t, "rfc5546/4.4.2-request.ics"), time.UTC)
	quirk := load(t, "quirks/rfc5546/4.4.5-this-and-future.ics")
	assert.False(t, quirk.Components[0].ThisAndFuture)
	got, changed = Apply(Request, series, quirk, time.UTC)
	require.True(t, changed)
	from, to := time.Date(1997, 8, 15, 0, 0, 0, 0, time.UTC), time.Date(1997, 10, 15, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, []string{"0901T21 Building 32, Microsoft, Seattle, WA", "1001T21 Conference Call"}, starts(t, got, from, to))
	assert.Contains(t, write(t, got.Encode(got.IANAZones)), "RECURRENCE-ID;THISANDFUTURE:19970901T210000Z\r\n")
}

// TestApplyReply records an attendee's answer on the organizer's copy.
func TestApplyReply(t *testing.T) {
	stored := load(t, "rfc5546/4.2.3-update.ics")
	got, changed := Apply(Reply, stored, load(t, "rfc5546/4.2.2-reply.ics"), time.UTC)
	require.True(t, changed)
	assert.Equal(t, "ACCEPTED", partstat(t, got.Master(), "mailto:b@example.com"))
	assert.Equal(t, "", partstat(t, stored.Master(), "mailto:b@example.com"), "the copy given was changed")

	// The same answer again changes nothing.
	_, changed = Apply(Reply, got, load(t, "rfc5546/4.2.2-reply.ics"), time.UTC)
	assert.False(t, changed)

	// An answer for one occurrence creates its override.
	series, _ := Apply(Request, nil, load(t, "rfc5546/4.4.2-request.ics"), time.UTC)
	reply := parse(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nMETHOD:REPLY\r\nBEGIN:VEVENT\r\nUID:guid-1@example.com\r\n"+
		"RECURRENCE-ID:19971001T210000Z\r\nORGANIZER:mailto:a@example.com\r\nATTENDEE;PARTSTAT=DECLINED:mailto:c@example.com\r\n"+
		"DTSTAMP:19970901T000000Z\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	got, changed = Apply(Reply, series, reply, time.UTC)
	require.True(t, changed)
	require.Equal(t, 2, len(got.Components))
	assert.Equal(t, "DECLINED", partstat(t, got.Components[1], "mailto:c@example.com"))
	assert.Equal(t, "", partstat(t, got.Master(), "mailto:c@example.com"))
	assert.Equal(t, "19971001T210000", got.Components[1].Start.Local)

	// Someone who was not invited cannot answer.
	crasher := strings.ReplaceAll(reply.Components[0].Attendees[0].Address, "c@", "z@")
	reply.Components[0].Attendees[0].Address = crasher
	_, changed = Apply(Reply, series, reply, time.UTC)
	assert.False(t, changed)
}

// organizer is a series by a@example.com with attendees, as stored in a's
// calendar.
const organizer = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:plan-1\r\nDTSTAMP:20261001T000000Z\r\nSEQUENCE:0\r\n" +
	"SUMMARY:Planning\r\nDTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\n" +
	"ORGANIZER;CN=A:mailto:a@example.com\r\n" +
	"ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:B@Example.com\r\n" +
	"ATTENDEE;SCHEDULE-AGENT=CLIENT:mailto:c@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:d@example.com\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:plan-1\r\nRECURRENCE-ID:20261109T090000Z\r\nDTSTAMP:20261001T000000Z\r\nSEQUENCE:0\r\n" +
	"SUMMARY:Planning (long)\r\nDTSTART:20261109T090000Z\r\nDTEND:20261109T120000Z\r\n" +
	"ORGANIZER;CN=A:mailto:a@example.com\r\n" +
	"ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:b@example.com\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func byRecipient(msgs []Message) map[string]Message {
	out := map[string]Message{}
	for _, m := range msgs {
		out[m.Recipient] = m
	}
	return out
}

func TestOrganize(t *testing.T) {
	self := is("mailto:a@example.com")
	o := parse(t, organizer)

	// A new event invites everyone the server schedules for: not the
	// organizer, not an attendee whose client schedules.
	msgs := byRecipient(Organize(nil, o, self, now))
	require.Equal(t, 2, len(msgs))
	b, d := msgs["mailto:b@example.com"], msgs["mailto:d@example.com"]
	assert.Equal(t, Request, b.Method)
	require.Equal(t, 2, len(b.Object.Components), "b is invited to the series and the override")
	assert.Equal(t, 0, len(b.Object.Components[0].Alarms))
	assert.Equal(t, "20261026T120000", b.Object.Components[0].DTStamp.Local)
	// d is not invited to the override: their copy excludes it.
	require.Equal(t, 1, len(d.Object.Components))
	assert.Equal(t, "20261109T090000", d.Object.Components[0].ExDates[0].Local)
	text := write(t, d.Calendar())
	assert.Contains(t, text, "METHOD:REQUEST\r\n")
	assert.NotContains(t, text, "SCHEDULE-AGENT")
	assert.NotContains(t, text, "VALARM")
	assert.Equal(t, 1, len(o.Master().Alarms), "the organizer's copy was changed")

	// A PARTSTAT changing is not news to anyone.
	n := o.Clone()
	n.Master().Attendees[1].PartStat = "ACCEPTED"
	assert.Equal(t, 0, len(Organize(o, n, self, now)))

	// Changing the override tells only those invited to it.
	n = o.Clone()
	n.Components[1].Location = "Room 2"
	msgs = byRecipient(Organize(o, n, self, now))
	assert.Equal(t, []string{"mailto:b@example.com"}, keys(msgs))

	// Left out of the series from the third week on, d's series ends
	// before it.
	n = o.Clone()
	future := n.Components[1].Clone()
	future.RecurrenceID = ical.DateTime{Local: "20261116T090000", TZID: "UTC"}
	future.Start, future.End = future.RecurrenceID, ical.DateTime{Local: "20261116T100000", TZID: "UTC"}
	future.ThisAndFuture = true
	n.Components = append(n.Components, future)
	d = byRecipient(Organize(o, n, self, now))["mailto:d@example.com"]
	require.Equal(t, 1, len(d.Object.Components))
	assert.Equal(t, "FREQ=WEEKLY;UNTIL=20261116T085959Z", d.Object.Components[0].RRule)
	assert.Equal(t, 1, len(d.Object.Components[0].ExDates))

	// SCHEDULE-FORCE-SEND sends anyway.
	n = o.Clone()
	n.Master().Attendees[3].Params = setParam(n.Master().Attendees[3].Params, paramForceSend, "REQUEST")
	assert.Equal(t, []string{"mailto:d@example.com"}, keys(byRecipient(Organize(o, n, self, now))))

	// Removing an attendee cancels for them, at a higher SEQUENCE; the
	// others hear who attends now.
	n = o.Clone()
	n.Master().Attendees = slices.Delete(n.Master().Attendees, 3, 4)
	msgs = byRecipient(Organize(o, n, self, now))
	require.Equal(t, []string{"mailto:b@example.com", "mailto:d@example.com"}, keys(msgs))
	assert.Equal(t, Request, msgs["mailto:b@example.com"].Method)
	assert.Equal(t, Cancel, msgs["mailto:d@example.com"].Method)
	assert.Equal(t, "CANCELLED", msgs["mailto:d@example.com"].Object.Components[0].Status)
	assert.Equal(t, 1, *msgs["mailto:d@example.com"].Object.Components[0].Sequence)

	// Cancelling the event, or deleting it, cancels for everyone.
	n = o.Clone()
	for _, e := range n.Components {
		e.Status = "CANCELLED"
	}
	msgs = byRecipient(Organize(o, n, self, now))
	assert.Equal(t, Cancel, msgs["mailto:b@example.com"].Method)
	assert.Equal(t, Cancel, msgs["mailto:d@example.com"].Method)
	msgs = byRecipient(Organize(o, nil, self, now))
	assert.Equal(t, 2, len(msgs))
	assert.Equal(t, Cancel, msgs["mailto:b@example.com"].Method)

	// Someone else's event is theirs to schedule.
	assert.Equal(t, 0, len(Organize(nil, o, is("mailto:b@example.com"), now)))
}

func keys(m map[string]Message) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestOrganizeRFCRecurring(t *testing.T) {
	// RFC 5546 §4.4.1 writes attendees without mailto: Cog cannot reach
	// them, but they are still recipients, and the client's own zone goes
	// with the message.
	o := load(t, "rfc5546/4.4.1-recurring-request.ics")
	msgs := byRecipient(Organize(nil, o, is("mailto:a@example.com", "a@example.com"), now))
	assert.Equal(t, []string{"b@example.fr", "c@example.jp"}, keys(msgs))
	assert.Contains(t, write(t, msgs["b@example.fr"].Calendar()), "TZID:America-SanJose\r\n")
}

func TestRespond(t *testing.T) {
	self := is("mailto:b@example.com")
	o := parse(t, organizer)

	// Accepting the series answers for the series.
	n := o.Clone()
	n.Master().Attendees[1].PartStat = "ACCEPTED"
	m := Respond(o, n, self, now)
	require.True(t, m != nil)
	assert.Equal(t, Reply, m.Method)
	assert.Equal(t, "mailto:a@example.com", m.Recipient)
	require.Equal(t, 1, len(m.Object.Components))
	e := m.Object.Components[0]
	assert.False(t, e.IsOverride())
	require.Equal(t, 1, len(e.Attendees))
	assert.Equal(t, "ACCEPTED", e.Attendees[0].PartStat)
	assert.Equal(t, "", e.Attendees[0].RSVP)
	assert.Equal(t, "A", e.Organizer.CN)

	// Declining one occurrence the attendee answered on their own.
	n = o.Clone()
	ov, err := n.Override(time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC), ical.ZonesFor(n, time.UTC))
	require.NoError(t, err)
	attendee(ov, "mailto:b@example.com").PartStat = "DECLINED"
	m = Respond(o, n, self, now)
	require.True(t, m != nil)
	require.Equal(t, 1, len(m.Object.Components))
	assert.Equal(t, "20261116T090000", m.Object.Components[0].RecurrenceID.Local)

	// Nothing answered, nothing sent; unless the organizer is forced.
	assert.True(t, Respond(o, o.Clone(), self, now) == nil)
	n = o.Clone()
	for _, e := range n.Components {
		e.Organizer.Params = setParam(e.Organizer.Params, paramForceSend, "REPLY")
	}
	m = Respond(o, n, self, now)
	require.True(t, m != nil)
	assert.Equal(t, 2, len(m.Object.Components))

	// Deleting the event declines it.
	m = Respond(o, nil, self, now)
	require.True(t, m != nil)
	for _, e := range m.Object.Components {
		assert.Equal(t, "DECLINED", e.Attendees[0].PartStat)
	}

	// An organizer whose client schedules gets nothing from the server.
	n = o.Clone()
	for _, e := range n.Components {
		e.Organizer.Params = setParam(e.Organizer.Params, paramAgent, "CLIENT")
	}
	n.Master().Attendees[1].PartStat = "ACCEPTED"
	assert.True(t, Respond(o, n, self, now) == nil)
}

func TestClean(t *testing.T) {
	old := parse(t, organizer)
	SetAttendeeStatus(old, "mailto:b@example.com", StatusDelivered)
	n := old.Clone()
	a := &n.Master().Attendees[3]
	a.Params = setParam(setParam(a.Params, paramStatus, "2.0"), paramForceSend, "REQUEST")
	n.Master().Attendees[1].Params = setParam(n.Master().Attendees[1].Params, paramStatus, "5.3")
	Clean(old, n)
	assert.Equal(t, "", param(a.Params, paramForceSend))
	assert.Equal(t, "", param(a.Params, paramStatus), "a client's status is not stored")
	assert.Equal(t, StatusDelivered, param(n.Master().Attendees[1].Params, paramStatus), "the server's status stays")
	assert.Equal(t, StatusDelivered, param(n.Components[1].Attendees[1].Params, paramStatus))
}

func TestBusyTime(t *testing.T) {
	self := is("mailto:me@example.com")
	ev := func(extra string) *ical.Object {
		return parse(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:x\r\nDTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z\r\n"+
			extra+"END:VEVENT\r\nEND:VCALENDAR\r\n")
	}
	from, to := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	busy := func(o *ical.Object, floating *time.Location) []Period {
		ps, err := BusyTime(o, ical.ZonesFor(o, floating), self, from, to)
		require.NoError(t, err)
		return ps
	}
	assert.Equal(t, []Period{{time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), Busy}}, busy(ev(""), time.UTC))
	assert.Equal(t, 0, len(busy(ev("TRANSP:TRANSPARENT\r\n"), time.UTC)))
	assert.Equal(t, 0, len(busy(ev("STATUS:CANCELLED\r\n"), time.UTC)))
	assert.Equal(t, BusyTentative, busy(ev("STATUS:TENTATIVE\r\n"), time.UTC)[0].Type)
	assert.Equal(t, 0, len(busy(ev("ATTENDEE;PARTSTAT=DECLINED:mailto:ME@example.com\r\n"), time.UTC)))
	assert.Equal(t, BusyTentative, busy(ev("ATTENDEE;PARTSTAT=NEEDS-ACTION:mailto:me@example.com\r\n"), time.UTC)[0].Type)
	assert.Equal(t, Busy, busy(ev("ATTENDEE;PARTSTAT=ACCEPTED:mailto:me@example.com\r\nATTENDEE;PARTSTAT=DECLINED:mailto:x@example.com\r\n"), time.UTC)[0].Type)

	// An all-day event is busy for its day where the calendar is.
	ny, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	day := parse(t, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nBEGIN:VEVENT\r\nUID:d\r\nDTSTART;VALUE=DATE:20261103\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	ps := busy(day, ny)
	require.Equal(t, 1, len(ps))
	assert.Equal(t, "2026-11-03T05:00:00Z 2026-11-04T05:00:00Z", ps[0].Start.UTC().Format(time.RFC3339)+" "+ps[0].End.UTC().Format(time.RFC3339))
}

func TestMergeAndFreeBusy(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 2, h, 0, 0, 0, time.UTC) }
	from, to := at(8), at(18)
	got := Merge([]Period{
		{at(9), at(10), Busy}, {at(10), at(11), Busy}, {at(7), at(9), Busy},
		{at(13), at(14), BusyTentative}, {at(13), at(15), BusyTentative}, {at(17), at(20), Busy},
	}, from, to)
	assert.Equal(t, []Period{{at(8), at(11), Busy}, {at(13), at(15), BusyTentative}, {at(17), at(18), Busy}}, got)
	text := write(t, Calendar("", FreeBusy(got, from, to, now)))
	assert.Contains(t, text, "DTSTART:20261102T080000Z\r\nDTEND:20261102T180000Z\r\n"+
		"FREEBUSY:20261102T080000Z/20261102T110000Z\r\nFREEBUSY;FBTYPE=BUSY-TENTATIVE:20261102T130000Z/20261102T150000Z\r\n")
	assert.NotContains(t, text, "METHOD")
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"MAILTO:Bob@Example.COM":                                  "mailto:bob@example.com",
		"https://cog.example/remote.php/dav/principals/users/bob": "/remote.php/dav/principals/users/bob/",
		"/remote.php/dav/principals/users/bob/":                   "/remote.php/dav/principals/users/bob/",
		"urn:uuid:room-1":                                         "urn:uuid:room-1",
	} {
		assert.Equal(t, want, Normalize(in))
	}
}
