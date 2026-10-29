package jmap_test

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/jmap"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func TestCalendarsFlag(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	env.Providers.JMAP.Calendars = false
	body := env.PostJSON(t, jmap.APIPath, obj{"using": []string{jmap.CapCore, jmap.CapContacts},
		"methodCalls": []any{[]any{"Calendar/get", obj{"accountId": acct(alice)}, "0"}}}, alice.Auth).Status(200).ReadBody()
	assert.Contains(t, body, "unknownMethod")
}

// TestCalendarEvents creates, reads, changes, queries and deletes events
// over JMAP, and checks DAV sees each step.
func TestCalendarEvents(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	setEmail(t, env, bob, "bob@example.com")
	cal := personal(t, env, alice)
	calID := "c" + cal.ID.String()

	cals := one(t, env, alice, "Calendar/get", obj{"accountId": acct(alice)})["list"].([]any)
	require.Equal(t, 1, len(cals))
	c0 := cals[0].(obj)
	assert.Equal[any](t, calID, c0["id"])
	assert.Equal[any](t, true, c0["isDefault"])
	assert.Equal[any](t, true, c0["myRights"].(obj)["mayWriteAll"])

	start := one(t, env, alice, "CalendarEvent/get", obj{"accountId": acct(alice), "ids": []any{}})["state"].(string)
	set := one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "create": obj{
		"e1": obj{
			"@type": "Event", "calendarIds": obj{calID: true}, "title": "Standup",
			"start": "2026-03-16T09:00:00", "timeZone": "Europe/Berlin", "duration": "PT15M",
			"recurrenceRules": []any{obj{"@type": "RecurrenceRule", "frequency": "daily", "count": 5}},
			"locations":       obj{"room": obj{"@type": "Location", "name": "Room 1", "coordinates": "geo:52.5,13.4"}},
			"alerts":          obj{"a1": obj{"@type": "Alert", "trigger": obj{"@type": "OffsetTrigger", "offset": "-PT5M"}}},
		},
		"task": obj{"@type": "Task", "calendarIds": obj{calID: true}, "title": "not here"},
	}})
	created := set["created"].(obj)["e1"].(obj)
	id := created["id"].(string)
	uid := created["uid"].(string)
	assert.Equal[any](t, "invalidProperties", set["notCreated"].(obj)["task"].(obj)["type"])

	ics := davReq(t, env, alice, "GET", "/remote.php/dav/calendars/alice/personal/"+uid+".ics", "").Status(200).ReadBody()
	assert.Contains(t, ics, "SUMMARY:Standup")
	assert.Contains(t, ics, "DTSTART;TZID=Europe/Berlin:20260316T090000")
	assert.Contains(t, ics, "DTEND;TZID=Europe/Berlin:20260316T091500")
	assert.Contains(t, ics, "RRULE:FREQ=DAILY;COUNT=5")
	assert.Contains(t, ics, "LOCATION;JSID=room:Room 1")
	assert.Contains(t, ics, "JSPROP;JSPTR=/locations/room/coordinates:")
	assert.Contains(t, ics, "TRIGGER:-PT5M")
	assert.Contains(t, ics, "BEGIN:VTIMEZONE")

	got := one(t, env, alice, "CalendarEvent/get", obj{"accountId": acct(alice), "ids": []any{id}})["list"].([]any)[0].(obj)
	assert.Equal[any](t, "geo:52.5,13.4", got["locations"].(obj)["room"].(obj)["coordinates"])
	assert.Equal[any](t, true, got["isOrigin"])
	assert.Equal[any](t, false, got["isDraft"])

	// One occurrence moves and one goes; the time change bumps SEQUENCE.
	env.Clock.Advance(time.Hour)
	upd := one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "update": obj{id: obj{
		"recurrenceOverrides": obj{
			"2026-03-17T09:00:00": obj{"start": "2026-03-17T10:00:00", "title": "Late standup"},
			"2026-03-18T09:00:00": obj{"excluded": true},
		},
	}}})
	assert.Nil(t, upd["notUpdated"])
	ics = davReq(t, env, alice, "GET", "/remote.php/dav/calendars/alice/personal/"+uid+".ics", "").Status(200).ReadBody()
	assert.Contains(t, ics, "EXDATE;TZID=Europe/Berlin:20260318T090000")
	assert.Contains(t, ics, "RECURRENCE-ID;TZID=Europe/Berlin:20260317T090000")
	assert.Contains(t, ics, "SUMMARY:Late standup")
	assert.Contains(t, ics, "DTSTART;TZID=Europe/Berlin:20260317T100000")

	// Query by time: the series overlaps March, not February.
	q := one(t, env, alice, "CalendarEvent/query", obj{"accountId": acct(alice),
		"filter": obj{"after": "2026-03-01T00:00:00Z", "before": "2026-04-01T00:00:00Z"}})
	assert.Equal[any](t, []any{id}, q["ids"])
	q = one(t, env, alice, "CalendarEvent/query", obj{"accountId": acct(alice),
		"filter": obj{"after": "2026-02-01T00:00:00Z", "before": "2026-03-01T00:00:00Z"}})
	assert.Equal[any](t, []any{}, q["ids"])
	q = one(t, env, alice, "CalendarEvent/query", obj{"accountId": acct(alice), "filter": obj{"title": "late"}})
	assert.Equal[any](t, []any{id}, q["ids"])
	assert.Equal[any](t, "invalidArguments", failing(t, env, alice, "CalendarEvent/query", obj{"accountId": acct(alice), "expandRecurrences": true}))

	ch := one(t, env, alice, "CalendarEvent/changes", obj{"accountId": acct(alice), "sinceState": start})
	assert.Equal[any](t, []any{id}, ch["created"])

	// A DAV write is a JMAP change, and the reverse.
	state := ch["newState"].(string)
	davReq(t, env, alice, "PUT", "/remote.php/dav/calendars/alice/personal/dav.ics",
		"BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//t//EN\r\nBEGIN:VEVENT\r\nUID:dav-1\r\nDTSTAMP:20260301T000000Z\r\n"+
			"DTSTART:20260320T120000Z\r\nDURATION:PT1H\r\nSUMMARY:From DAV\r\nX-CUSTOM;P=1:kept\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n").Status(201)
	ch = one(t, env, alice, "CalendarEvent/changes", obj{"accountId": acct(alice), "sinceState": state})
	require.Equal(t, 1, len(ch["created"].([]any)))
	davID := ch["created"].([]any)[0].(string)
	one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "update": obj{davID: obj{"title": "Renamed"}}})
	ics = davReq(t, env, alice, "GET", "/remote.php/dav/calendars/alice/personal/dav.ics", "").Status(200).ReadBody()
	assert.Contains(t, ics, "SUMMARY:Renamed")
	assert.Contains(t, ics, "X-CUSTOM;P=1:kept")
	assert.Contains(t, ics, "DURATION:PT1H")

	// Inviting bob sends nothing unless the client asks.
	inv := func(send bool) {
		one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "sendSchedulingMessages": send, "update": obj{davID: obj{
			"replyTo":      obj{"imip": "mailto:alice@example.com"},
			"participants": obj{"o": obj{"@type": "Participant", "sendTo": obj{"imip": "mailto:alice@example.com"}, "roles": obj{"owner": true}}, "b": obj{"@type": "Participant", "sendTo": obj{"imip": "mailto:bob@example.com"}, "roles": obj{"attendee": true}}},
		}}})
	}
	setEmail(t, env, alice, "alice@example.com")
	inv(false)
	bobCal := personal(t, env, bob)
	got2 := one(t, env, bob, "CalendarEvent/get", obj{"accountId": acct(bob)})
	assert.Equal(t, 0, len(got2["list"].([]any)))
	one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "update": obj{davID: obj{"title": "Meeting"}}, "sendSchedulingMessages": true})
	got2 = one(t, env, bob, "CalendarEvent/get", obj{"accountId": acct(bob)})
	require.Equal(t, 1, len(got2["list"].([]any)))
	ev := got2["list"].([]any)[0].(obj)
	assert.Equal[any](t, "Meeting", ev["title"])
	assert.Equal[any](t, false, ev["isOrigin"])
	assert.Equal[any](t, obj{"c" + bobCal.ID.String(): true}, ev["calendarIds"])

	// Destroy.
	del := one(t, env, alice, "CalendarEvent/set", obj{"accountId": acct(alice), "destroy": []any{id}})
	assert.Equal[any](t, []any{id}, del["destroyed"])
	davReq(t, env, alice, "GET", "/remote.php/dav/calendars/alice/personal/"+uid+".ics", "").Status(404)
}

func TestCalendarSet(t *testing.T) {
	env := newEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	personal(t, env, alice)
	set := one(t, env, alice, "Calendar/set", obj{"accountId": acct(alice), "create": obj{
		"c":   obj{"name": "Work", "color": "#ff0000", "timeZone": "Europe/Paris"},
		"bad": obj{"name": "x", "timeZone": "Not/AZone"},
	}})
	id := set["created"].(obj)["c"].(obj)["id"].(string)
	assert.Equal[any](t, []any{"timeZone"}, set["notCreated"].(obj)["bad"].(obj)["properties"])
	list := one(t, env, alice, "Calendar/get", obj{"accountId": acct(alice), "ids": []any{id}})["list"].([]any)
	c := list[0].(obj)
	assert.Equal[any](t, "Work", c["name"])
	assert.Equal[any](t, "#ff0000", c["color"])
	assert.Equal[any](t, "Europe/Paris", c["timeZone"])
	// DAV lists it.
	body := davReq(t, env, alice, "PROPFIND", "/remote.php/dav/calendars/alice/", `<D:propfind xmlns:D="DAV:"><D:prop><D:displayname/></D:prop></D:propfind>`, "Depth", "1").Status(207).ReadBody()
	assert.Contains(t, body, "<D:displayname>Work</D:displayname>")
	assert.True(t, strings.Contains(body, "/work/"), body)
	des := one(t, env, alice, "Calendar/set", obj{"accountId": acct(alice), "destroy": []any{id}})
	assert.Equal[any](t, []any{id}, des["destroyed"])
}

func setEmail(t *testing.T, env *cogtest.Env, u *cogtest.User, email string) {
	t.Helper()
	require.NoError(t, env.Providers.Users.Update(u.ID, user.UpdateOpts{Email: &email}))
}
