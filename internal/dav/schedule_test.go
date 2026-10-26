package dav_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
	"git.duckfam.us/jj-software/cog/plugin"
)

// unfold joins folded lines, so assertions need not know where a long line
// breaks.
func unfold(s string) string { return strings.ReplaceAll(s, "\r\n ", "") }

// calendarData reads calendar text out of an XML response.
func calendarData(s string) string {
	return unfold(strings.NewReplacer("&#xD;", "\r", "&#xA;", "\n").Replace(s))
}

func withEmail(t *testing.T, env *cogtest.Env, u *cogtest.User, email string) {
	t.Helper()
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{Email: &email}))
}

// schedEnv has alice (a@example.com) and bob (b@example.com); carol is not
// a Cog user.
func schedEnv(t *testing.T) (*cogtest.Env, *cogtest.User, *cogtest.User) {
	t.Helper()
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	withEmail(t, env, alice, "a@example.com")
	withEmail(t, env, bob, "B@Example.com")
	personal(t, env, alice)
	personal(t, env, bob)
	return env, alice, bob
}

const meeting = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:meet-1\r\nDTSTAMP:20261020T000000Z\r\nSEQUENCE:0\r\nSUMMARY:Planning\r\n" +
	"DTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\n" +
	"ORGANIZER;CN=Alice:mailto:a@example.com\r\n" +
	"ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:b@example.com\r\n" +
	"ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:carol@elsewhere.example\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT10M\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func inboxItems(t *testing.T, env *cogtest.Env, u *cogtest.User) []string {
	t.Helper()
	body := req(t, env, u, "PROPFIND", cal+u.Username+"/inbox/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><D:prop><C:calendar-data/></D:prop></D:propfind>`, "Depth", "1").
		Status(207).ReadBody()
	var out []string
	for _, part := range strings.Split(body, "<C:calendar-data>")[1:] {
		data, _, _ := strings.Cut(part, "</C:calendar-data>")
		out = append(out, calendarData(data))
	}
	return out
}

func TestSchedulingDiscovery(t *testing.T) {
	env, alice, _ := schedEnv(t)

	body := req(t, env, alice, "PROPFIND", "/remote.php/dav/principals/users/alice/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:prop><C:schedule-inbox-URL/><C:schedule-outbox-URL/><C:calendar-user-address-set/></D:prop></D:propfind>`).Status(207).ReadBody()
	assert.Contains(t, body, "<C:schedule-inbox-URL><D:href>/remote.php/dav/calendars/alice/inbox/</D:href></C:schedule-inbox-URL>")
	assert.Contains(t, body, "<C:schedule-outbox-URL><D:href>/remote.php/dav/calendars/alice/outbox/</D:href></C:schedule-outbox-URL>")
	assert.Contains(t, body, "<D:href>mailto:a@example.com</D:href>")

	opts := req(t, env, alice, "OPTIONS", cal+"alice/personal/", "").Status(200)
	assert.Contains(t, opts.Header.Get("DAV"), "calendar-auto-schedule")

	body = req(t, env, alice, "PROPFIND", cal+"alice/inbox/", "", "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<C:schedule-inbox/>")
	assert.Contains(t, body, "<C:schedule-default-calendar-URL><D:href>/remote.php/dav/calendars/alice/personal/</D:href>")
	assert.Contains(t, body, "<C:schedule-deliver/>")
	body = req(t, env, alice, "PROPFIND", cal+"alice/outbox/", "", "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<C:schedule-outbox/>")
	assert.Contains(t, body, "<C:schedule-send-freebusy/>")

	// The home lists both beside the calendars.
	body = req(t, env, alice, "PROPFIND", cal+"alice/", `<D:propfind xmlns:D="DAV:"><D:prop><D:resourcetype/></D:prop></D:propfind>`, "Depth", "1").Status(207).ReadBody()
	assert.Contains(t, body, "<D:href>/remote.php/dav/calendars/alice/inbox/</D:href>")
	assert.Contains(t, body, "<D:href>/remote.php/dav/calendars/alice/outbox/</D:href>")

	// The names are taken, and nobody else may look inside.
	req(t, env, alice, "MKCALENDAR", cal+"alice/inbox/", "").Status(405)
	bob := env.CreateUser(t, "bob2", "pass")
	req(t, env, bob, "PROPFIND", cal+"alice/inbox/", "", "Depth", "0").Status(403)
	req(t, env, alice, "PUT", cal+"alice/inbox/x.ics", ics("VEVENT", "x", "Nope")).Status(403)
}

// TestSchedulingBetweenUsers follows one meeting from alice's invitation to
// its cancellation, through bob's calendar and both inboxes; carol, outside
// Cog, gets email.
func TestSchedulingBetweenUsers(t *testing.T) {
	env, alice, bob := schedEnv(t)
	aliceEv, bobEv := cal+"alice/personal/meet-1.ics", cal+"bob/personal/meet-1.ics"

	// The invitation: Cog records what it did with each attendee, so the
	// stored copy differs from the request and the PUT returns no ETag.
	put := req(t, env, alice, "PUT", aliceEv, meeting).Status(201)
	assert.Equal(t, "", put.Header.Get("ETag"))
	got := unfold(req(t, env, alice, "GET", aliceEv, "").Status(200).ReadBody())
	assert.Contains(t, got, "SCHEDULE-STATUS=1.2:mailto:b@example.com\r\n")
	assert.Contains(t, got, "SCHEDULE-STATUS=1.1:mailto:carol@elsewhere.example\r\n")
	assert.NotContains(t, got, "SCHEDULE-STATUS=1.2:mailto:a@example.com")

	mail := env.Mailbox.ForRecipient("carol@elsewhere.example")
	require.Equal(t, 1, len(mail))
	assert.Equal(t, "Invitation: Planning", mail[0].Subject)
	assert.Contains(t, string(mail[0].Raw), "method=REQUEST")

	// Bob has it in his calendar at once, without alice's alarm or the
	// scheduling parameters, and the message is in his inbox.
	copyOf := unfold(req(t, env, bob, "GET", bobEv, "").Status(200).ReadBody())
	assert.Contains(t, copyOf, "SUMMARY:Planning\r\n")
	assert.NotContains(t, copyOf, "VALARM")
	assert.NotContains(t, copyOf, "SCHEDULE-")
	assert.NotContains(t, copyOf, "METHOD")
	items := inboxItems(t, env, bob)
	require.Equal(t, 1, len(items))
	assert.Contains(t, items[0], "METHOD:REQUEST\r\n")

	// Bob accepts in his client, with an alarm of his own: alice's copy
	// learns his answer, and the REPLY reaches her inbox.
	accepted := strings.Replace(copyOf, "PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:b@example.com", "PARTSTAT=ACCEPTED:mailto:b@example.com", 1)
	accepted = strings.Replace(accepted, "END:VEVENT", "BEGIN:VALARM\r\nACTION:AUDIO\r\nTRIGGER:-PT1H\r\nEND:VALARM\r\nEND:VEVENT", 1)
	require.True(t, accepted != copyOf)
	req(t, env, bob, "PUT", bobEv, accepted).Status(204)
	got = unfold(req(t, env, alice, "GET", aliceEv, "").Status(200).ReadBody())
	assert.Contains(t, got, "PARTSTAT=ACCEPTED;RSVP=TRUE;SCHEDULE-STATUS=1.2:mailto:b@example.com\r\n")
	replies := inboxItems(t, env, alice)
	require.Equal(t, 1, len(replies))
	assert.Contains(t, replies[0], "METHOD:REPLY\r\n")
	assert.Contains(t, req(t, env, bob, "GET", bobEv, "").ReadBody(), "SCHEDULE-STATUS=1.2")

	// Alice moves the series and drops the third week: bob's copy follows,
	// keeping his alarm and his answer.
	moved := strings.Replace(got, "DTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z", "DTSTART:20261102T140000Z\r\nDTEND:20261102T150000Z", 1)
	moved = strings.Replace(moved, "SEQUENCE:0", "SEQUENCE:1", 1)
	moved = strings.Replace(moved, "RRULE:FREQ=WEEKLY;COUNT=4\r\n", "RRULE:FREQ=WEEKLY;COUNT=4\r\nEXDATE:20261116T140000Z\r\n", 1)
	require.True(t, moved != got)
	req(t, env, alice, "PUT", aliceEv, moved).Status(204)
	copyOf = unfold(req(t, env, bob, "GET", bobEv, "").Status(200).ReadBody())
	assert.Contains(t, copyOf, "DTSTART:20261102T140000Z\r\n")
	assert.Contains(t, copyOf, "EXDATE:20261116T140000Z\r\n")
	assert.Contains(t, copyOf, "TRIGGER:-PT1H\r\n")
	assert.Contains(t, copyOf, "PARTSTAT=ACCEPTED")
	assert.Equal(t, 2, len(inboxItems(t, env, bob)))
	assert.Equal(t, 2, len(env.Mailbox.ForRecipient("carol@elsewhere.example")))

	// Changing the last weeks from the fourth on (RANGE=THISANDFUTURE)
	// reaches bob as sent.
	got = unfold(req(t, env, alice, "GET", aliceEv, "").ReadBody())
	future := strings.Replace(got, "END:VCALENDAR", "BEGIN:VEVENT\r\nUID:meet-1\r\nRECURRENCE-ID;RANGE=THISANDFUTURE:20261123T140000Z\r\n"+
		"DTSTAMP:20261021T000000Z\r\nSEQUENCE:2\r\nSUMMARY:Planning (new room)\r\nLOCATION:Room 2\r\n"+
		"DTSTART:20261123T140000Z\r\nDTEND:20261123T150000Z\r\nORGANIZER;CN=Alice:mailto:a@example.com\r\n"+
		"ATTENDEE;ROLE=CHAIR;PARTSTAT=ACCEPTED:mailto:a@example.com\r\nATTENDEE;PARTSTAT=ACCEPTED:mailto:b@example.com\r\n"+
		"END:VEVENT\r\nEND:VCALENDAR", 1)
	req(t, env, alice, "PUT", aliceEv, future).Status(204)
	copyOf = unfold(req(t, env, bob, "GET", bobEv, "").Status(200).ReadBody())
	assert.Contains(t, copyOf, "RECURRENCE-ID;RANGE=THISANDFUTURE:20261123T140000Z\r\n")
	assert.Contains(t, copyOf, "LOCATION:Room 2\r\n")
	// Carol is not invited to the override: her copy excludes it.
	last := env.Mailbox.ForRecipient("carol@elsewhere.example")
	require.Equal(t, 3, len(last))

	// Alice deletes the meeting: bob's copy is cancelled, carol is told.
	req(t, env, alice, "DELETE", aliceEv, "").Status(204)
	copyOf = req(t, env, bob, "GET", bobEv, "").Status(200).ReadBody()
	assert.Equal(t, 2, strings.Count(copyOf, "STATUS:CANCELLED\r\n"))
	last = env.Mailbox.ForRecipient("carol@elsewhere.example") // newest first
	require.Equal(t, 4, len(last))
	assert.Equal(t, "Cancelled: Planning", last[0].Subject)

	// Bob deleting his copy declines it; alice has nothing left to update,
	// but the REPLY still reaches her inbox. Schedule-Reply: F would not.
	n := len(inboxItems(t, env, alice))
	req(t, env, bob, "DELETE", bobEv, "").Status(204)
	items = inboxItems(t, env, alice)
	require.Equal(t, n+1, len(items))
	declined := 0
	for _, it := range items {
		if strings.Contains(it, "METHOD:REPLY") && strings.Contains(it, "PARTSTAT=DECLINED") {
			declined++
		}
	}
	assert.Equal(t, 1, declined)
}

func TestSchedulingDeleteWithoutReply(t *testing.T) {
	env, alice, bob := schedEnv(t)
	req(t, env, alice, "PUT", cal+"alice/personal/meet-1.ics", meeting).Status(201)
	req(t, env, bob, "DELETE", cal+"bob/personal/meet-1.ics", "", "Schedule-Reply", "F").Status(204)
	assert.Equal(t, 0, len(inboxItems(t, env, alice)))
	got := req(t, env, alice, "GET", cal+"alice/personal/meet-1.ics", "").ReadBody()
	assert.NotContains(t, got, "DECLINED")
}

func TestScheduleAgentAndForceSend(t *testing.T) {
	env, alice, bob := schedEnv(t)
	path := cal + "alice/personal/meet-1.ics"
	clientSide := strings.Replace(meeting, "ATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:b@example.com",
		"ATTENDEE;SCHEDULE-AGENT=CLIENT;PARTSTAT=NEEDS-ACTION:mailto:b@example.com", 1)
	req(t, env, alice, "PUT", path, clientSide).Status(201)
	req(t, env, bob, "GET", cal+"bob/personal/meet-1.ics", "").Status(404)
	assert.Equal(t, 0, len(inboxItems(t, env, bob)))
	got := unfold(req(t, env, alice, "GET", path, "").ReadBody())
	assert.Contains(t, got, "SCHEDULE-AGENT=CLIENT")

	// The same object again, forced for bob: sent once, and the parameter
	// is not stored.
	forced := strings.Replace(meeting, "RSVP=TRUE:mailto:b@example.com", "RSVP=TRUE;SCHEDULE-FORCE-SEND=REQUEST:mailto:b@example.com", 1)
	req(t, env, alice, "PUT", path, meeting).Status(204)
	assert.Equal(t, 1, len(inboxItems(t, env, bob)))
	req(t, env, alice, "PUT", path, meeting).Status(204)
	assert.Equal(t, 1, len(inboxItems(t, env, bob)), "nothing changed, nothing sent")
	req(t, env, alice, "PUT", path, forced).Status(204)
	assert.Equal(t, 2, len(inboxItems(t, env, bob)))
	assert.NotContains(t, req(t, env, alice, "GET", path, "").ReadBody(), "FORCE-SEND")

	// A client cannot set SCHEDULE-STATUS: the server's stays.
	lie := strings.Replace(meeting, "RSVP=TRUE:mailto:b@example.com", "RSVP=TRUE;SCHEDULE-STATUS=5.3:mailto:b@example.com", 1)
	req(t, env, alice, "PUT", path, lie).Status(204)
	got = unfold(req(t, env, alice, "GET", path, "").ReadBody())
	assert.Contains(t, got, "SCHEDULE-STATUS=1.2:mailto:b@example.com")
	assert.NotContains(t, got, "5.3")
}

// TestShareeSchedulesForOwner: a sharee writing in the owner's calendar
// schedules as the owner only with the Schedule permission.
func TestShareeSchedulesForOwner(t *testing.T) {
	env, alice, bob := schedEnv(t)
	carol := env.CreateUser(t, "carol", "pass")
	withEmail(t, env, carol, "carol@example.com")
	personal(t, env, carol)
	inviteCarol := strings.ReplaceAll(meeting, "carol@elsewhere.example", "carol@example.com")
	alices := personal(t, env, alice)

	share(t, env, alice, bob, alices.ID, plugin.PermRead|plugin.PermWrite)
	body := req(t, env, bob, "PROPFIND", cal+"alice/personal/", "", "Depth", "0").ReadBody()
	assert.NotContains(t, body, "schedule-send")
	req(t, env, bob, "PUT", cal+"alice/personal/meet-1.ics", inviteCarol).Status(201)
	req(t, env, carol, "GET", cal+"carol/personal/meet-1.ics", "").Status(404)
	got := unfold(req(t, env, alice, "GET", cal+"alice/personal/meet-1.ics", "").ReadBody())
	assert.Contains(t, got, "SCHEDULE-STATUS=3.8:mailto:carol@example.com")

	share(t, env, alice, bob, alices.ID, plugin.PermRead|plugin.PermWrite|plugin.PermSchedule)
	body = req(t, env, bob, "PROPFIND", cal+"alice/personal/", "", "Depth", "0").ReadBody()
	assert.Contains(t, body, "<C:schedule-send/>")
	req(t, env, bob, "PUT", cal+"alice/personal/meet-1.ics", strings.Replace(inviteCarol, "SEQUENCE:0", "SEQUENCE:1", 1)).Status(204)
	copyOf := req(t, env, carol, "GET", cal+"carol/personal/meet-1.ics", "").Status(200).ReadBody()
	assert.Contains(t, copyOf, "ORGANIZER;CN=Alice:mailto:a@example.com")
	got = unfold(req(t, env, alice, "GET", cal+"alice/personal/meet-1.ics", "").ReadBody())
	assert.Contains(t, got, "SCHEDULE-STATUS=1.2:mailto:carol@example.com")
}

func TestInboxSyncAndDelete(t *testing.T) {
	env, alice, bob := schedEnv(t)
	_, tok := syncReport(t, env, bob, cal+"bob/inbox/", "")
	req(t, env, alice, "PUT", cal+"alice/personal/meet-1.ics", meeting).Status(201)
	body, tok2 := syncReport(t, env, bob, cal+"bob/inbox/", tok)
	_, rest, ok := strings.Cut(body, "<D:href>/remote.php/dav/calendars/bob/inbox/")
	require.True(t, ok, body)
	name, _, _ := strings.Cut(rest, "</D:href>")
	req(t, env, bob, "GET", cal+"bob/inbox/"+name, "").Status(200).Contains("METHOD:REQUEST")

	req(t, env, bob, "DELETE", cal+"bob/inbox/"+name, "").Status(204)
	body, _ = syncReport(t, env, bob, cal+"bob/inbox/", tok2)
	assert.Contains(t, body, name+"</D:href><D:status>HTTP/1.1 404 Not Found</D:status>")
	// Deleting the message leaves the event.
	req(t, env, bob, "GET", cal+"bob/personal/meet-1.ics", "").Status(200)
}

const busyEvents = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:busy-1\r\nDTSTAMP:20261020T000000Z\r\nSUMMARY:Dentist\r\nDTSTART:20261102T090000Z\r\nDTEND:20261102T100000Z\r\n" +
	"RRULE:FREQ=DAILY;COUNT=2\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"

const freeEvent = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" +
	"BEGIN:VEVENT\r\nUID:free-1\r\nDTSTAMP:20261020T000000Z\r\nSUMMARY:Reminder\r\nTRANSP:TRANSPARENT\r\nDTSTART:20261102T120000Z\r\nDTEND:20261102T130000Z\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

const fbQuery = `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"><C:time-range start="20261101T000000Z" end="20261108T000000Z"/></C:free-busy-query>`

func TestFreeBusyQuery(t *testing.T) {
	env, alice, bob := schedEnv(t)
	alices := personal(t, env, alice)
	req(t, env, alice, "PUT", cal+"alice/personal/busy.ics", busyEvents).Status(201)
	req(t, env, alice, "PUT", cal+"alice/personal/free.ics", freeEvent).Status(201)

	got := req(t, env, alice, "REPORT", cal+"alice/personal/", fbQuery).Status(200)
	assert.Contains(t, got.Header.Get("Content-Type"), "text/calendar")
	body := got.ReadBody()
	assert.Contains(t, body, "BEGIN:VFREEBUSY\r\n")
	assert.Contains(t, body, "DTSTART:20261101T000000Z\r\nDTEND:20261108T000000Z\r\n")
	assert.Contains(t, body, "FREEBUSY:20261102T090000Z/20261102T100000Z\r\nFREEBUSY:20261103T090000Z/20261103T100000Z\r\n")
	assert.NotContains(t, body, "20261102T120000Z")
	assert.NotContains(t, body, "Dentist")

	// Without a share, bob may not ask; with a free/busy share he may, and
	// sees nothing else.
	req(t, env, bob, "REPORT", cal+"alice/personal/", fbQuery).Status(403)
	share(t, env, alice, bob, alices.ID, plugin.PermFreeBusy)
	req(t, env, bob, "REPORT", cal+"alice/personal/", fbQuery).Status(200).Contains("FREEBUSY:20261102T090000Z/20261102T100000Z")
	req(t, env, bob, "REPORT", cal+"alice/personal/", `<C:calendar-query xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:D="DAV:">
<D:prop><C:calendar-data/></D:prop><C:filter><C:comp-filter name="VCALENDAR"/></C:filter></C:calendar-query>`).Status(207).NotContains("Dentist")

	req(t, env, alice, "REPORT", cal+"alice/personal/", `<C:free-busy-query xmlns:C="urn:ietf:params:xml:ns:caldav"/>`).Status(400)
}

func outboxRequest(organizer string, attendees ...string) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nMETHOD:REQUEST\r\nBEGIN:VFREEBUSY\r\nUID:fb-1\r\n" +
		"DTSTAMP:20261026T000000Z\r\nDTSTART:20261101T000000Z\r\nDTEND:20261108T000000Z\r\nORGANIZER:" + organizer + "\r\n")
	for _, a := range attendees {
		b.WriteString("ATTENDEE:" + a + "\r\n")
	}
	b.WriteString("END:VFREEBUSY\r\nEND:VCALENDAR\r\n")
	return b.String()
}

func TestOutboxFreeBusy(t *testing.T) {
	env, alice, bob := schedEnv(t)
	carol := env.CreateUser(t, "carol", "pass")
	withEmail(t, env, carol, "carol@example.com")
	alices := personal(t, env, alice)
	req(t, env, alice, "PUT", cal+"alice/personal/busy.ics", busyEvents).Status(201)
	req(t, env, bob, "PUT", cal+"bob/personal/busy.ics", strings.NewReplacer("T09", "T15", "T10", "T16").Replace(busyEvents)).Status(201)
	share(t, env, alice, bob, alices.ID, plugin.PermFreeBusy)

	got := req(t, env, bob, "POST", cal+"bob/outbox/", outboxRequest("mailto:b@example.com",
		"mailto:a@example.com", "mailto:carol@example.com", "mailto:nobody@example.com", "mailto:b@example.com"),
		"Content-Type", "text/calendar").Status(200)
	body := calendarData(got.ReadBody())
	responses := strings.Split(body, "<C:response>")[1:]
	require.Equal(t, 4, len(responses))
	assert.Contains(t, responses[0], "<D:href>mailto:a@example.com</D:href>")
	assert.Contains(t, responses[0], "<C:request-status>2.0;Success</C:request-status>")
	assert.Contains(t, responses[0], "METHOD:REPLY\r\n")
	assert.Contains(t, responses[0], "UID:fb-1\r\n")
	assert.Contains(t, responses[0], "FREEBUSY:20261102T090000Z/20261102T100000Z\r\n")
	assert.Contains(t, responses[1], "<C:request-status>3.8;No authority</C:request-status>")
	assert.NotContains(t, responses[1], "calendar-data")
	assert.Contains(t, responses[2], "<C:request-status>3.7;Invalid calendar user</C:request-status>")
	assert.Contains(t, responses[3], "FREEBUSY:20261102T150000Z/20261102T160000Z\r\n")

	// A calendar its owner marked transparent makes nobody busy.
	req(t, env, alice, "PROPPATCH", cal+"alice/personal/", `<D:propertyupdate xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><C:schedule-calendar-transp><C:transparent/></C:schedule-calendar-transp></D:prop></D:set></D:propertyupdate>`).Status(207)
	body = req(t, env, bob, "POST", cal+"bob/outbox/", outboxRequest("mailto:b@example.com", "mailto:a@example.com")).Status(200).ReadBody()
	assert.NotContains(t, body, "FREEBUSY:")

	// Only for oneself, and only free/busy.
	req(t, env, bob, "POST", cal+"bob/outbox/", outboxRequest("mailto:a@example.com", "mailto:b@example.com")).Status(403).Contains("organizer-allowed")
	req(t, env, bob, "POST", cal+"bob/outbox/", meeting).Status(400)
	req(t, env, alice, "POST", cal+"bob/outbox/", outboxRequest("mailto:a@example.com", "mailto:b@example.com")).Status(403)
}
