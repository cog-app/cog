package dav_test

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/dav"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
	"git.duckfam.us/jj-software/cog/plugin/contacts"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

// davEnv runs a server with every plugin that holds DAV resources.
func davEnv(t *testing.T) *cogtest.Env {
	t.Helper()
	return cogtest.New(t, calendar.New(), tasks.New(), contacts.New())
}

func req(t *testing.T, env *cogtest.Env, u *cogtest.User, method, path, body string, headers ...string) *cogtest.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	rq := cogtest.NewRequest(t, method, env.Providers.Config.BaseURL+path, r)
	for i := 0; i+1 < len(headers); i += 2 {
		rq.Header.Set(headers[i], headers[i+1])
	}
	return env.Do(t, rq, u.Auth)
}

func ics(comp, uid, summary string) string {
	extra := "DTSTART:20261025T090000Z\r\nDTEND:20261025T100000Z\r\n"
	if comp == "VTODO" {
		extra = "STATUS:NEEDS-ACTION\r\n"
	}
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:" + comp + "\r\nUID:" + uid +
		"\r\nSUMMARY:" + summary + "\r\n" + extra + "END:" + comp + "\r\nEND:VCALENDAR\r\n"
}

func vcard(uid, fn string) string {
	return "BEGIN:VCARD\r\nVERSION:3.0\r\nUID:" + uid + "\r\nFN:" + fn + "\r\nEND:VCARD\r\n"
}

const cal = "/remote.php/dav/calendars/"

// syncReport sends sync-collection with token and returns body and new token.
func syncReport(t *testing.T, env *cogtest.Env, u *cogtest.User, path, token string) (string, string) {
	t.Helper()
	body := req(t, env, u, "REPORT", path, `<?xml version="1.0"?>
<D:sync-collection xmlns:D="DAV:"><D:sync-token>`+token+`</D:sync-token><D:sync-level>1</D:sync-level>
<D:prop><D:getetag/></D:prop></D:sync-collection>`).Status(207).ReadBody()
	_, rest, ok := strings.Cut(body, "<D:sync-token>")
	require.True(t, ok, body)
	next, _, _ := strings.Cut(rest, "</D:sync-token>")
	return body, next
}

func TestDiscovery(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")

	req(t, env, alice, "PROPFIND", "/remote.php/dav/", `<D:propfind xmlns:D="DAV:"><D:prop><D:current-user-principal/></D:prop></D:propfind>`, "Depth", "0").
		Status(207).Contains("<D:href>/remote.php/dav/principals/users/alice/</D:href>")

	body := req(t, env, alice, "PROPFIND", "/remote.php/dav/principals/users/alice/", `<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:CR="urn:ietf:params:xml:ns:carddav" xmlns:X="urn:x">
<D:prop><C:calendar-home-set/><CR:addressbook-home-set/><X:unknown/></D:prop></D:propfind>`).Status(207).ReadBody()
	assert.Contains(t, body, "<C:calendar-home-set><D:href>/remote.php/dav/calendars/alice/</D:href></C:calendar-home-set>")
	assert.Contains(t, body, "<CR:addressbook-home-set><D:href>/remote.php/dav/addressbooks/alice/</D:href></CR:addressbook-home-set>")
	// An unknown property is a 404 propstat, not dropped.
	assert.Contains(t, body, `<unknown xmlns="urn:x"/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status>`)

	// propname lists names without values.
	body = req(t, env, alice, "PROPFIND", cal+"alice/", `<D:propfind xmlns:D="DAV:"><D:propname/></D:propfind>`, "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<D:displayname/>")
	assert.NotContains(t, body, "<D:displayname>")

	// .well-known points clients at the root.
	env.Get(t, "/.well-known/caldav", alice.Auth, cogtest.StopRedirect).Status(301)
}

func TestMixedCollectionHoldsEventsAndTasks(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")

	req(t, env, alice, "PUT", cal+"alice/personal/ev.ics", ics("VEVENT", "ev-1", "Meeting")).Status(201)
	req(t, env, alice, "PUT", cal+"alice/personal/todo.ics", ics("VTODO", "todo-1", "Chore")).Status(201)

	body := req(t, env, alice, "PROPFIND", cal+"alice/personal/", "", "Depth", "1").Status(207).ReadBody()
	assert.Contains(t, body, `<C:supported-calendar-component-set><C:comp name="VEVENT"/><C:comp name="VTODO"/></C:supported-calendar-component-set>`)
	assert.Contains(t, body, "component=vevent")
	assert.Contains(t, body, "component=vtodo")

	req(t, env, alice, "GET", cal+"alice/personal/todo.ics", "").Status(200).Contains("SUMMARY:Chore")

	// Each plugin's UI sees its own kind in the same collection.
	plug, _ := env.Providers.Plugins.Plugin("tasks")
	tp := plug.(*tasks.Plugin)
	lists, err := tp.Lists(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(lists))
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)
	got, err := tasks.ListTasks(db, lists[0].ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	assert.Equal(t, "Chore", got[0].Summary)

	// A journal has no place in a collection that does not hold journals.
	req(t, env, alice, "PUT", cal+"alice/personal/j.ics", ics("VJOURNAL", "j-1", "Note")).
		Status(403).Contains("supported-calendar-component")
}

func TestCompositeSyncAcrossBackends(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	path := cal + "alice/personal/"

	req(t, env, alice, "PUT", path+"old.ics", ics("VEVENT", "old", "Old")).Status(201)
	body, token := syncReport(t, env, alice, path, "")
	assert.Contains(t, body, "old.ics")
	assert.Contains(t, token, "-e1")
	assert.Contains(t, token, "-t0")

	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "New")).Status(201)
	req(t, env, alice, "PUT", path+"todo.ics", ics("VTODO", "todo", "Task")).Status(201)
	body, token = syncReport(t, env, alice, path, token)
	assert.Contains(t, body, "ev.ics")
	assert.Contains(t, body, "todo.ics")
	assert.NotContains(t, body, "old.ics")

	req(t, env, alice, "DELETE", path+"todo.ics", "").Status(204)
	body, _ = syncReport(t, env, alice, path, token)
	assert.Contains(t, body, "<D:href>/remote.php/dav/calendars/alice/personal/todo.ics</D:href><D:status>HTTP/1.1 404 Not Found</D:status>")
	assert.NotContains(t, body, "ev.ics")

	// Tokens from the future, or not ours, make the client resync.
	req(t, env, alice, "REPORT", path, `<D:sync-collection xmlns:D="DAV:"><D:sync-token>http://cog/ns/sync/v2-e99</D:sync-token><D:prop/></D:sync-collection>`).
		Status(403).Contains("valid-sync-token")
	req(t, env, alice, "REPORT", path, `<D:sync-collection xmlns:D="DAV:"><D:sync-token>urn:other</D:sync-token><D:prop/></D:sync-collection>`).
		Status(403).Contains("valid-sync-token")
}

func TestWebEditsReachSync(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	path := cal + "alice/personal/"
	req(t, env, alice, "PUT", path+"ev.ics", ics("VEVENT", "ev", "Event")).Status(201)
	_, token := syncReport(t, env, alice, path, "")

	// Deleting from the web UI leaves a tombstone (it used to vanish silently).
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)
	c, err := cp.CalendarByName(alice.ID, "personal")
	require.NoError(t, err)
	ev, err := calendar.GetEventByUID(db, c.ID, "ev")
	require.NoError(t, err)
	require.NoError(t, calendar.DeleteEventByID(db, ev.ID, env.Now()))
	body, token := syncReport(t, env, alice, path, token)
	assert.Contains(t, body, "ev.ics</D:href><D:status>HTTP/1.1 404 Not Found")

	// Deleting a task from the web deletes its subtasks, each with a tombstone.
	tplug, _ := env.Providers.Plugins.Plugin("tasks")
	tdb, err := tplug.(*tasks.Plugin).DB(alice.Username)
	require.NoError(t, err)
	parent, err := tasks.CreateTask(tdb, c.ID, "parent", nil, "Parent", "", 0, nil, nil, env.Now())
	require.NoError(t, err)
	_, err = tasks.CreateTask(tdb, c.ID, "child", &parent.ID, "Child", "", 0, nil, nil, env.Now())
	require.NoError(t, err)
	_, token = syncReport(t, env, alice, path, token)
	require.NoError(t, tasks.DeleteTask(tdb, parent.ID, env.Now()))
	body, _ = syncReport(t, env, alice, path, token)
	assert.Contains(t, body, "parent.ics</D:href><D:status>HTTP/1.1 404")
	assert.Contains(t, body, "child.ics</D:href><D:status>HTTP/1.1 404")
}

func TestTombstoneRetention(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	require.NoError(t, env.Providers.Settings.SetInt(settings.KeyDAVTombstoneDays, 1))
	path := cal + "alice/personal/"
	req(t, env, alice, "PUT", path+"a.ics", ics("VEVENT", "a", "A")).Status(201)
	_, token := syncReport(t, env, alice, path, "")
	req(t, env, alice, "DELETE", path+"a.ics", "").Status(204)

	// Within the window the deletion is reported.
	syncReport(t, env, alice, path, token)

	// Once it is pruned, the old token can no longer be answered.
	env.Clock.Advance(48 * time.Hour)
	req(t, env, alice, "REPORT", path, `<D:sync-collection xmlns:D="DAV:"><D:sync-token>`+token+`</D:sync-token><D:prop/></D:sync-collection>`).
		Status(403).Contains("valid-sync-token")
	// A fresh sync works.
	syncReport(t, env, alice, path, "")
}

func TestPutPreconditionsAndETags(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	path := cal + "alice/personal/x.ics"

	put := req(t, env, alice, "PUT", path, ics("VEVENT", "x", "One"), "If-None-Match", "*").Status(201)
	etag := put.Header.Get("ETag")
	require.True(t, strings.HasPrefix(etag, `"`) && strings.Contains(etag, "-"), etag)
	req(t, env, alice, "GET", path, "").Status(200)
	assert.Equal(t, etag, req(t, env, alice, "GET", path, "").Header.Get("ETag"))

	// Create-only fails once it exists.
	req(t, env, alice, "PUT", path, ics("VEVENT", "x", "Two"), "If-None-Match", "*").Status(412)
	// A stale or foreign ETag fails; the current one succeeds.
	req(t, env, alice, "PUT", path, ics("VEVENT", "x", "Two"), "If-Match", `"999-1"`).Status(412)
	next := req(t, env, alice, "PUT", path, ics("VEVENT", "x", "Two"), "If-Match", etag).Status(204).Header.Get("ETag")
	assert.True(t, next != etag, "ETag must change on write")
	req(t, env, alice, "DELETE", path, "", "If-Match", etag).Status(412)
	req(t, env, alice, "GET", path, "", "If-None-Match", next).Status(304)
	req(t, env, alice, "DELETE", path, "", "If-Match", next).Status(204)
	req(t, env, alice, "GET", path, "").Status(404)
}

func TestResourceNameIsNotTheUID(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	path := cal + "alice/personal/"

	// Clients choose the name; it used to be taken as the UID.
	req(t, env, alice, "PUT", path+"a.ics", ics("VEVENT", "b", "Named a")).Status(201)
	req(t, env, alice, "GET", path+"a.ics", "").Status(200).Contains("UID:b")
	req(t, env, alice, "GET", path+"b.ics", "").Status(404)
	req(t, env, alice, "REPORT", path, `<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:prop><D:getetag/><C:calendar-data/></D:prop><D:href>`+path+`a.ics</D:href><D:href>`+path+`missing.ics</D:href></C:calendar-multiget>`).
		Status(207).Contains("Named a").Contains("missing.ics</D:href><D:status>HTTP/1.1 404")

	// A UID lives at one name per collection, across backends too.
	req(t, env, alice, "PUT", path+"c.ics", ics("VEVENT", "b", "Dup")).Status(403).Contains("no-uid-conflict").Contains(path + "a.ics")
	req(t, env, alice, "PUT", path+"t.ics", ics("VTODO", "b", "Dup")).Status(403).Contains("no-uid-conflict")
	// A name keeps its component type.
	req(t, env, alice, "PUT", path+"a.ics", ics("VTODO", "z", "Retyped")).Status(409)

	// Names with spaces are escaped in hrefs and found again.
	req(t, env, alice, "PUT", path+"with%20space.ics", ics("VEVENT", "sp", "Space")).Status(201)
	req(t, env, alice, "PROPFIND", path, "", "Depth", "1").Status(207).Contains(path + "with%20space.ics")
	req(t, env, alice, "GET", path+"with%20space.ics", "").Status(200)

	// Not a calendar object.
	req(t, env, alice, "PUT", path+"bad.ics", "hello").Status(403).Contains("valid-calendar-data")
	mixed := strings.Replace(ics("VEVENT", "m", "M"), "END:VCALENDAR", "BEGIN:VEVENT\r\nUID:other\r\nEND:VEVENT\r\nEND:VCALENDAR", 1)
	req(t, env, alice, "PUT", path+"mixed.ics", mixed).Status(403).Contains("valid-calendar-object-resource")
}

func TestMakeCollections(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")

	// MKCALENDAR with a component set makes a task list.
	req(t, env, alice, "MKCALENDAR", cal+"alice/chores/", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><D:displayname>Chores</D:displayname>
<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`).Status(201)
	body := req(t, env, alice, "PROPFIND", cal+"alice/chores/", "", "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, `<C:supported-calendar-component-set><C:comp name="VTODO"/></C:supported-calendar-component-set>`)
	assert.Contains(t, body, "<D:displayname>Chores</D:displayname>")
	req(t, env, alice, "PUT", cal+"alice/chores/e.ics", ics("VEVENT", "e", "E")).Status(403)

	// Journals, for jtx Board.
	req(t, env, alice, "MKCALENDAR", cal+"alice/journal/", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><C:supported-calendar-component-set><C:comp name="VJOURNAL"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`).Status(201)
	req(t, env, alice, "PUT", cal+"alice/journal/j.ics", ics("VJOURNAL", "j", "Day")).Status(201)
	req(t, env, alice, "GET", cal+"alice/journal/j.ics", "").Status(200).Contains("BEGIN:VJOURNAL")

	// A set naming something no calendar holds is refused; so is a name in use.
	req(t, env, alice, "MKCALENDAR", cal+"alice/odd/", `<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><C:supported-calendar-component-set><C:comp name="VCARD"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`).
		Status(403).Contains("supported-calendar-component")
	req(t, env, alice, "MKCALENDAR", cal+"alice/chores/", "").Status(405)

	// Extended MKCOL (RFC 5689) for an address book and for a calendar.
	req(t, env, alice, "MKCOL", "/remote.php/dav/addressbooks/alice/friends/", `<D:mkcol xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav">
<D:set><D:prop><D:resourcetype><D:collection/><CR:addressbook/></D:resourcetype><D:displayname>Friends</D:displayname></D:prop></D:set></D:mkcol>`).Status(201)
	req(t, env, alice, "PUT", "/remote.php/dav/addressbooks/alice/friends/f.vcf", vcard("f", "Friend")).Status(201)
	req(t, env, alice, "MKCOL", cal+"alice/work/", `<D:mkcol xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><D:resourcetype><D:collection/><C:calendar/></D:resourcetype></D:prop></D:set></D:mkcol>`).Status(201)
	// A plain collection is not something a calendar home can hold.
	req(t, env, alice, "MKCOL", cal+"alice/plain/", "").Status(403).Contains("valid-resourcetype")
	// Nobody else can create in alice's home.
	bob := env.CreateUser(t, "bob", "pass")
	req(t, env, bob, "MKCALENDAR", cal+"alice/bobs/", "").Status(403)
}

func TestProppatch(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	path := cal + "alice/personal/"
	req(t, env, alice, "PROPFIND", path, "", "Depth", "0").Status(207)
	_, token := syncReport(t, env, alice, path, "")
	ctag := func() string {
		body := req(t, env, alice, "PROPFIND", path, `<D:propfind xmlns:D="DAV:" xmlns:CS="http://calendarserver.org/ns/"><D:prop><CS:getctag/></D:prop></D:propfind>`, "Depth", "0").ReadBody()
		_, rest, _ := strings.Cut(body, "<CS:getctag>")
		v, _, _ := strings.Cut(rest, "</CS:getctag>")
		return v
	}
	before := ctag()

	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:" xmlns:I="http://apple.com/ns/ical/" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:X="urn:vendor">
<D:set><D:prop><D:displayname>Home</D:displayname><I:calendar-color>#ff0000FF</I:calendar-color><I:calendar-order>3</I:calendar-order>
<C:calendar-timezone-id>America/Denver</C:calendar-timezone-id><X:shown><X:flag on="1">yes</X:flag></X:shown></D:prop></D:set></D:propertyupdate>`).
		Status(207).Contains("200 OK").NotContains("403")

	body := req(t, env, alice, "PROPFIND", path, "", "Depth", "0").Status(207).ReadBody()
	assert.Contains(t, body, "<D:displayname>Home</D:displayname>")
	assert.Contains(t, body, "<ICAL:calendar-order>3</ICAL:calendar-order>")
	assert.Contains(t, body, "<C:calendar-timezone-id>America/Denver</C:calendar-timezone-id>")
	// A property Cog does not model is kept and returned, namespaces intact.
	assert.Contains(t, body, `<shown xmlns="urn:vendor"><flag xmlns="urn:vendor" on="1">yes</flag></shown>`)
	// getctag notices property changes; the sync token does not need to.
	assert.True(t, ctag() != before)
	syncReport(t, env, alice, path, token)

	// One protected property fails the whole request.
	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Nope</D:displayname><D:getetag>"x"</D:getetag></D:prop></D:set></D:propertyupdate>`).
		Status(207).Contains("403 Forbidden").Contains("424 Failed Dependency").Contains("cannot-modify-protected-property")
	req(t, env, alice, "PROPFIND", path, "", "Depth", "0").Contains("<D:displayname>Home</D:displayname>")

	// Removing the dead property drops it.
	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:" xmlns:X="urn:vendor"><D:remove><D:prop><X:shown/></D:prop></D:remove></D:propertyupdate>`).Status(207)
	req(t, env, alice, "PROPFIND", path, "", "Depth", "0").NotContains("urn:vendor")
}

// share makes owner share their collection id with subject.
func share(t *testing.T, env *cogtest.Env, owner, subject *cogtest.User, id dav.CollectionID, perms uint32) {
	t.Helper()
	_, err := env.Providers.Shares.Create(owner.ID, dav.ShareType, id.String(), "USER", subject.ID, perms)
	require.NoError(t, err)
}

func personal(t *testing.T, env *cogtest.Env, u *cogtest.User) *dav.Collection {
	t.Helper()
	_, err := env.Providers.DAV.Collections.EnsureDefault(u.ID, dav.HomeCalendars)
	require.NoError(t, err)
	c, err := env.Providers.DAV.Collections.Get(u.ID, dav.HomeCalendars, "personal")
	require.NoError(t, err)
	return c
}

func TestSharing(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	carol := env.CreateUser(t, "carol", "pass")
	bobs := personal(t, env, bob)
	req(t, env, bob, "PUT", cal+"bob/personal/ev.ics", ics("VEVENT", "ev", "Bob's secret")).Status(201)
	path := cal + "bob/personal/"

	// No share, no access.
	req(t, env, alice, "GET", path+"ev.ics", "").Status(403)

	// A share by someone who does not own the collection grants nothing,
	// even though it names the right ID.
	share(t, env, carol, alice, bobs.ID, plugin.PermRead|plugin.PermWrite)
	req(t, env, alice, "GET", path+"ev.ics", "").Status(403)

	// Read-only: listed in alice's home under bob's path, readable, not
	// writable, and the privilege set says so.
	share(t, env, bob, alice, bobs.ID, plugin.PermRead)
	body := req(t, env, alice, "PROPFIND", cal+"alice/", "", "Depth", "1").Status(207).ReadBody()
	_, sharedPart, ok := strings.Cut(body, "<D:href>"+path+"</D:href>")
	require.True(t, ok, body)
	sharedPart, _, _ = strings.Cut(sharedPart, "</D:response>")
	assert.Contains(t, sharedPart, "<D:privilege><D:read/></D:privilege>")
	assert.NotContains(t, sharedPart, "<D:write-content/>")
	assert.NotContains(t, sharedPart, "<D:all/>")
	assert.Contains(t, sharedPart, "Personal (bob)")
	req(t, env, alice, "GET", path+"ev.ics", "").Status(200).Contains("Bob's secret")
	req(t, env, alice, "PUT", path+"new.ics", ics("VEVENT", "n", "N")).Status(403)
	req(t, env, alice, "DELETE", path+"ev.ics", "").Status(403)

	// Read/write: alice may change resources, but the collection stays bob's.
	share(t, env, bob, alice, bobs.ID, plugin.PermRead|plugin.PermWrite)
	req(t, env, alice, "PUT", path+"new.ics", ics("VEVENT", "n", "From alice")).Status(201)
	req(t, env, alice, "PROPPATCH", path, `<D:propertyupdate xmlns:D="DAV:"><D:set><D:prop><D:displayname>Mine</D:displayname></D:prop></D:set></D:propertyupdate>`).Status(403)
	req(t, env, alice, "DELETE", path, "").Status(403)
	req(t, env, bob, "GET", path+"new.ics", "").Status(200).Contains("From alice")
	// Alice's own home is hers alone.
	req(t, env, alice, "PROPFIND", cal+"bob/", "", "Depth", "1").Status(403)
}

func TestFreeBusyShareSeesOnlyBusy(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	bobs := personal(t, env, bob)
	req(t, env, bob, "PUT", cal+"bob/personal/ev.ics", ics("VEVENT", "ev", "Doctor")).Status(201)
	share(t, env, bob, alice, bobs.ID, plugin.PermFreeBusy)

	got := req(t, env, alice, "GET", cal+"bob/personal/ev.ics", "").Status(200).ReadBody()
	assert.Contains(t, got, "SUMMARY:Busy")
	assert.NotContains(t, got, "Doctor")
	body := req(t, env, alice, "PROPFIND", cal+"bob/personal/", "", "Depth", "0").ReadBody()
	assert.Contains(t, body, "<C:read-free-busy/>")
	assert.NotContains(t, body, "<D:read/>")
}

func TestSharedAddressBookWriter(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	books, err := env.Providers.DAV.Collections.EnsureDefault(bob.ID, dav.HomeAddressBooks)
	require.NoError(t, err)
	share(t, env, bob, alice, books[0].ID, plugin.PermRead|plugin.PermWrite)

	// Writers can use a shared address book (they used to get 405).
	path := "/remote.php/dav/addressbooks/bob/contacts/"
	req(t, env, alice, "PUT", path+"x.vcf", vcard("x", "Shared Person")).Status(201)
	req(t, env, alice, "REPORT", path, `<CR:addressbook-query xmlns:D="DAV:" xmlns:CR="urn:ietf:params:xml:ns:carddav"><D:prop><CR:address-data/></D:prop></CR:addressbook-query>`).
		Status(207).Contains("Shared Person")
	req(t, env, alice, "DELETE", path+"x.vcf", "").Status(204)
}

func TestDeleteCollectionPurgesEveryBackend(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	c := personal(t, env, alice)
	req(t, env, alice, "PUT", cal+"alice/personal/e.ics", ics("VEVENT", "e", "E")).Status(201)
	req(t, env, alice, "PUT", cal+"alice/personal/t.ics", ics("VTODO", "t", "T")).Status(201)

	req(t, env, alice, "DELETE", cal+"alice/personal/", "").Status(204)
	_, err := env.Providers.DAV.Collections.ByID(c.ID)
	require.True(t, errors.Is(err, dav.ErrNotFound), err)

	// Both plugins dropped their rows.
	cplug, _ := env.Providers.Plugins.Plugin("calendar")
	cdb, err := cplug.(*calendar.Plugin).DB(alice.Username)
	require.NoError(t, err)
	events, err := calendar.ListEvents(cdb, int64(c.ID))
	require.NoError(t, err)
	assert.Equal(t, 0, len(events))
	tplug, _ := env.Providers.Plugins.Plugin("tasks")
	tdb, err := tplug.(*tasks.Plugin).DB(alice.Username)
	require.NoError(t, err)
	left, err := tasks.ListTasks(tdb, int64(c.ID))
	require.NoError(t, err)
	assert.Equal(t, 0, len(left))
	// The default comes back, with a new ID and nothing in it.
	again := personal(t, env, alice)
	assert.True(t, again.ID != c.ID)
	body, _ := syncReport(t, env, alice, cal+"alice/personal/", "")
	assert.NotContains(t, body, "e.ics")
	assert.NotContains(t, body, "t.ics")
}

func TestImportAndExport(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	c := personal(t, env, alice)

	file := "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//x//EN\r\nMETHOD:PUBLISH\r\nX-WR-TIMEZONE:America/Denver\r\n" +
		"BEGIN:VTIMEZONE\r\nTZID:Custom/Zone\r\nX-LIC-LOCATION:America/Denver\r\nEND:VTIMEZONE\r\n" +
		"BEGIN:VEVENT\r\nUID:series\r\nSUMMARY:Weekly\r\nDTSTART;TZID=Custom/Zone:20261005T090000\r\nRRULE:FREQ=WEEKLY\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:series\r\nRECURRENCE-ID;TZID=Custom/Zone:20261012T090000\r\nSUMMARY:Moved\r\nDTSTART;TZID=Custom/Zone:20261012T100000\r\nEND:VEVENT\r\n" +
		"BEGIN:VTODO\r\nUID:task/with slash\r\nSUMMARY:Todo\r\nEND:VTODO\r\n" +
		"BEGIN:VJOURNAL\r\nUID:journal\r\nSUMMARY:Diary\r\nEND:VJOURNAL\r\n" +
		"BEGIN:VEVENT\r\nSUMMARY:No UID\r\nDTSTART:20261001T090000Z\r\nEND:VEVENT\r\n" +
		"END:VCALENDAR\r\n"
	res, err := env.Providers.DAV.Import(alice.Username, c, strings.NewReader(file))
	require.NoError(t, err)
	assert.Equal(t, 3, res.Imported) // series with its override, the task, the UID-less event
	assert.Equal(t, 1, res.Skipped)  // personal holds no journals
	assert.Equal(t, 0, res.Failed)

	// The override stays with its master, with the zone it uses and no METHOD.
	body := req(t, env, alice, "GET", cal+"alice/personal/series.ics", "").Status(200).ReadBody()
	assert.Contains(t, body, "RECURRENCE-ID")
	// The zone named by X-LIC-LOCATION is stored and written as IANA, with a
	// generated VTIMEZONE.
	assert.Contains(t, body, "TZID:America/Denver\r\n")
	assert.Contains(t, body, "RECURRENCE-ID;TZID=America/Denver:20261012T090000")
	assert.Contains(t, body, "X-WR-TIMEZONE:America/Denver")
	assert.NotContains(t, body, "METHOD")

	// Importing again replaces rather than duplicates.
	res, err = env.Providers.DAV.Import(alice.Username, c, strings.NewReader(file))
	require.NoError(t, err)
	assert.Equal(t, 2, res.Replaced) // the UID-less event gets a new UID each time
	assert.Equal(t, 1, res.Imported)

	var out strings.Builder
	require.NoError(t, env.Providers.DAV.Export(&out, alice.Username, c))
	exported := out.String()
	assert.Equal(t, 1, strings.Count(exported, "BEGIN:VTIMEZONE"))
	assert.Equal(t, 1, strings.Count(exported, "BEGIN:VCALENDAR"))
	assert.Contains(t, exported, "SUMMARY:Todo")
	assert.Contains(t, exported, "X-WR-CALNAME:Personal")
}

func TestImportExportPage(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(alice.ID)
	require.NoError(t, err)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", auth.CSRFToken(sess.ID, env.Providers.CSRFSecret))

	env.Get(t, "/settings/data", cookie).Status(200).Contains("Personal").Contains("Contacts").Contains("New address book")
	jar := env.WithCookieJar(t)

	upload := func(target, name, filename, content string) *cogtest.Response {
		var body strings.Builder
		body.WriteString("--b\r\nContent-Disposition: form-data; name=\"target\"\r\n\r\n" + target + "\r\n")
		body.WriteString("--b\r\nContent-Disposition: form-data; name=\"name\"\r\n\r\n" + name + "\r\n")
		body.WriteString("--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + filename + "\"\r\nContent-Type: text/plain\r\n\r\n" + content + "\r\n--b--\r\n")
		rq := cogtest.NewRequest(t, "POST", env.Providers.Config.BaseURL+"/settings/data/import", strings.NewReader(body.String()))
		rq.Header.Set("Content-Type", "multipart/form-data; boundary=b")
		// Follow the redirect with the flash cookie, as a browser would.
		return jar.Do(t, rq, cookie, csrf)
	}
	upload("new:addressbooks", "Family", "family.vcf", vcard("mum", "Mum")+vcard("dad", "Dad")).
		Status(200).Contains("Imported into Family: 2 new")
	fam, err := env.Providers.DAV.Collections.Get(alice.ID, dav.HomeAddressBooks, "family")
	require.NoError(t, err)

	env.Get(t, "/settings/data/export/"+fam.ID.String(), cookie).Status(200).Contains("FN:Mum").Contains("FN:Dad")
	env.Get(t, "/settings/data/export.zip", cookie).Status(200)

	// Someone else's collection is not found.
	bob := env.CreateUser(t, "bob", "pass")
	bsess, err := env.Providers.Sessions.Create(bob.ID)
	require.NoError(t, err)
	env.Get(t, "/settings/data/export/"+fam.ID.String(), cogtest.Header("Cookie", auth.SessionCookieName+"="+bsess.ID)).Status(404)

	upload(fmt.Sprint(fam.ID), "", "bad.vcf", "garbage").Status(200).Contains("not an iCalendar or vCard file")
}

// TestSeeds runs the dev seeds, which go tool dev runs concurrently for all
// plugins, and reads the result back over DAV.
func TestSeeds(t *testing.T) {
	env := davEnv(t)
	alice := env.CreateUser(t, "alice", "pass").Seed(t)

	body := req(t, env, alice, "PROPFIND", cal+"alice/", "", "Depth", "1").Status(207).ReadBody()
	assert.Contains(t, body, cal+"alice/personal/")
	assert.Contains(t, body, cal+"alice/work/")
	assert.Contains(t, body, cal+"alice/work-tasks/")
	body, _ = syncReport(t, env, alice, cal+"alice/personal/", "")
	assert.Contains(t, body, "team-standup.ics")
	// Four events from the calendar seed and five tasks from the tasks seed.
	assert.Equal(t, 9, strings.Count(body, "<D:response>"))
	body, _ = syncReport(t, env, alice, "/remote.php/dav/addressbooks/alice/contacts/", "")
	assert.Contains(t, body, "alice-smith.vcf")
}
