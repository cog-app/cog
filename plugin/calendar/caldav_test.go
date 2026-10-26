package calendar_test

import (
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

func TestCalDAV_PrincipalDiscovery(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	body := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:current-user-principal/>
  </D:prop>
</D:propfind>`

	req := cogtest.NewRequest(t, "PROPFIND", env.Providers.Config.BaseURL+"/remote.php/dav/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	resp := env.Do(t, req, alice.Auth).Status(207)
	respBody := resp.ReadBody()
	require.True(t, strings.Contains(respBody, "/remote.php/dav/principals/users/alice/"))
	require.True(t, strings.Contains(respBody, "current-user-principal"))
}

func TestCalDAV_CalendarHome(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	body := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <C:calendar-home-set/>
  </D:prop>
</D:propfind>`

	req := cogtest.NewRequest(t, "PROPFIND", env.Providers.Config.BaseURL+"/remote.php/dav/principals/users/alice/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	resp := env.Do(t, req, alice.Auth).Status(207)
	respBody := resp.ReadBody()
	require.True(t, strings.Contains(respBody, "/remote.php/dav/calendars/alice/"))
	require.True(t, strings.Contains(respBody, "calendar-home-set"))
}

func TestCalDAV_PutGetDeleteEvent(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Ensure the user's calendar DB is migrated and has a default calendar.
	ensurePersonal(t, env, alice)

	now := time.Now().UTC()
	dtstart := now.Add(1 * time.Hour)
	dtend := now.Add(2 * time.Hour)
	ics := calendar.GenerateICS("caldav-test-uid", "CalDAV Meeting", "Room 101", "Office",
		dtstart, dtend, false, now)

	eventURL := env.Providers.Config.BaseURL + "/remote.php/dav/calendars/alice/personal/caldav-test-uid.ics"

	// PUT the event.
	putReq := cogtest.NewRequest(t, "PUT", eventURL, strings.NewReader(ics))
	putReq.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	putResp := env.Do(t, putReq, alice.Auth).Status(201)
	etag := putResp.Header.Get("ETag")
	require.True(t, etag != "")

	// GET the event back.
	getReq := cogtest.NewRequest(t, "GET", eventURL, nil)
	resp := env.Do(t, getReq, alice.Auth).Status(200)
	body := resp.ReadBody()
	require.True(t, strings.Contains(body, "CalDAV Meeting"))
	require.True(t, strings.Contains(body, "caldav-test-uid"))

	// DELETE the event.
	delReq := cogtest.NewRequest(t, "DELETE", eventURL, nil)
	env.Do(t, delReq, alice.Auth).Status(204)

	// Verify it's gone.
	getReq2 := cogtest.NewRequest(t, "GET", eventURL, nil)
	env.Do(t, getReq2, alice.Auth).Status(404)
}

func TestCalDAV_ReportMultiget(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	ensurePersonal(t, env, alice)

	now := time.Now().UTC()

	// PUT two events via CalDAV.
	for _, uid := range []string{"multiget-1", "multiget-2"} {
		ics := calendar.GenerateICS(uid, "Event "+uid, "", "",
			now.Add(1*time.Hour), now.Add(2*time.Hour), false, now)
		putReq := cogtest.NewRequest(t, "PUT",
			env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/"+uid+".ics",
			strings.NewReader(ics))
		putReq.Header.Set("Content-Type", "text/calendar; charset=utf-8")
		env.Do(t, putReq, alice.Auth).Status(201)
	}

	// Send a calendar-multiget REPORT.
	reportBody := `<?xml version="1.0" encoding="UTF-8"?>
<C:calendar-multiget xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
  <D:href>/remote.php/dav/calendars/alice/personal/multiget-1.ics</D:href>
  <D:href>/remote.php/dav/calendars/alice/personal/multiget-2.ics</D:href>
</C:calendar-multiget>`

	req := cogtest.NewRequest(t, "REPORT",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/",
		strings.NewReader(reportBody))
	req.Header.Set("Content-Type", "application/xml")

	resp := env.Do(t, req, alice.Auth).Status(207)
	body := resp.ReadBody()
	require.True(t, strings.Contains(body, "multiget-1"))
	require.True(t, strings.Contains(body, "multiget-2"))
	require.True(t, strings.Contains(body, "calendar-data"))
}

func TestCalDAV_SyncCollection(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	ensurePersonal(t, env, alice)

	now := time.Now().UTC()

	// PUT an event.
	ics := calendar.GenerateICS("sync-uid-1", "Sync Event", "", "",
		now.Add(1*time.Hour), now.Add(2*time.Hour), false, now)
	putReq := cogtest.NewRequest(t, "PUT",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/sync-uid-1.ics",
		strings.NewReader(ics))
	putReq.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	env.Do(t, putReq, alice.Auth).Status(201)

	// Send a sync-collection REPORT with empty sync-token (full sync).
	reportBody := `<?xml version="1.0" encoding="UTF-8"?>
<D:sync-collection xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:sync-token/>
  <D:prop>
    <D:getetag/>
    <C:calendar-data/>
  </D:prop>
</D:sync-collection>`

	req := cogtest.NewRequest(t, "REPORT",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/",
		strings.NewReader(reportBody))
	req.Header.Set("Content-Type", "application/xml")

	resp := env.Do(t, req, alice.Auth).Status(207)
	body := resp.ReadBody()
	require.True(t, strings.Contains(body, "sync-uid-1"))
	require.True(t, strings.Contains(body, "sync-token"))
}

func TestCalDAV_CalendarHomeListing(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	ensurePersonal(t, env, alice)

	// PROPFIND the calendar home with Depth: 1 to list calendars.
	body := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:ICAL="http://apple.com/ns/ical/">
  <D:prop>
    <D:displayname/>
    <ICAL:calendar-color/>
    <D:resourcetype/>
  </D:prop>
</D:propfind>`

	req := cogtest.NewRequest(t, "PROPFIND",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")

	resp := env.Do(t, req, alice.Auth).Status(207)
	respBody := resp.ReadBody()
	require.True(t, strings.Contains(respBody, "Personal"))
	require.True(t, strings.Contains(respBody, "calendar"))
}

func TestCalDAV_Options(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	ensurePersonal(t, env, alice)

	req := cogtest.NewRequest(t, "OPTIONS",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/",
		nil)
	resp := env.Do(t, req, alice.Auth).Status(200)
	allow := resp.Header.Get("Allow")
	require.True(t, strings.Contains(allow, "PROPFIND"))
	require.True(t, strings.Contains(allow, "REPORT"))
	dav := resp.Header.Get("DAV")
	require.True(t, strings.Contains(dav, "calendar-access"))
}

func TestCalDAV_MKCalendar(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	cp := ensurePersonal(t, env, alice)

	mkBody := `<?xml version="1.0" encoding="UTF-8"?>
<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav" xmlns:ICAL="http://apple.com/ns/ical/">
  <D:set>
    <D:prop>
      <D:displayname>Work</D:displayname>
      <ICAL:calendar-color>#ff0000</ICAL:calendar-color>
      <C:calendar-description>Work events</C:calendar-description>
    </D:prop>
  </D:set>
</C:mkcalendar>`

	req := cogtest.NewRequest(t, "MKCALENDAR",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/work-cal/",
		strings.NewReader(mkBody))
	req.Header.Set("Content-Type", "application/xml")
	env.Do(t, req, alice.Auth).Status(201)

	// Verify calendar was created via store.
	cal, err := cp.CalendarByName(alice.ID, "work-cal")
	require.NoError(t, err)
	require.Equal(t, "Work", cal.Name)
	require.Equal(t, "#ff0000", cal.Color)
}

func TestCalDAV_DeleteCalendar(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	cp := ensurePersonal(t, env, alice)

	// Verify "personal" calendar exists.
	cal, err := cp.CalendarByName(alice.ID, "personal")
	require.NoError(t, err)
	require.Equal(t, "Personal", cal.Name)

	// DELETE the calendar via CalDAV.
	req := cogtest.NewRequest(t, "DELETE",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/",
		nil)
	env.Do(t, req, alice.Auth).Status(204)

	// Verify it's gone.
	_, err = cp.CalendarByName(alice.ID, "personal")
	require.True(t, err != nil)
}

func TestCalDAV_Proppatch(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	cp := ensurePersonal(t, env, alice)

	proppatchBody := `<?xml version="1.0" encoding="UTF-8"?>
<D:propertyupdate xmlns:D="DAV:" xmlns:ICAL="http://apple.com/ns/ical/" xmlns:C="urn:ietf:params:xml:ns:caldav">
  <D:set>
    <D:prop>
      <D:displayname>Renamed</D:displayname>
      <ICAL:calendar-color>#00ff00</ICAL:calendar-color>
    </D:prop>
  </D:set>
</D:propertyupdate>`

	req := cogtest.NewRequest(t, "PROPPATCH",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/",
		strings.NewReader(proppatchBody))
	req.Header.Set("Content-Type", "application/xml")
	env.Do(t, req, alice.Auth).Status(207)

	// Verify changes.
	cal, err := cp.CalendarByName(alice.ID, "personal")
	require.NoError(t, err)
	require.Equal(t, "Renamed", cal.Name)
	require.Equal(t, "#00ff00", cal.Color)
}

func TestCalDAV_EventPropfind(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	ensurePersonal(t, env, alice)

	now := time.Now().UTC()
	ics := calendar.GenerateICS("propfind-uid", "Propfind Event", "", "",
		now.Add(1*time.Hour), now.Add(2*time.Hour), false, now)

	// PUT the event first.
	putReq := cogtest.NewRequest(t, "PUT",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/propfind-uid.ics",
		strings.NewReader(ics))
	putReq.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	env.Do(t, putReq, alice.Auth).Status(201)

	// PROPFIND on the event.
	pfBody := `<?xml version="1.0" encoding="UTF-8"?>
<D:propfind xmlns:D="DAV:">
  <D:prop>
    <D:getetag/>
    <D:getcontenttype/>
  </D:prop>
</D:propfind>`

	req := cogtest.NewRequest(t, "PROPFIND",
		env.Providers.Config.BaseURL+"/remote.php/dav/calendars/alice/personal/propfind-uid.ics",
		strings.NewReader(pfBody))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "0")

	resp := env.Do(t, req, alice.Auth).Status(207)
	body := resp.ReadBody()
	require.True(t, strings.Contains(body, "propfind-uid"))
	require.True(t, strings.Contains(body, "getetag"))
}

// ensurePersonal returns the calendar plugin after giving alice her default
// calendar.
func ensurePersonal(t *testing.T, env *cogtest.Env, u *cogtest.User) *calendar.Plugin {
	t.Helper()
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	_, err := cp.Calendars(u.ID)
	require.NoError(t, err)
	return cp
}
