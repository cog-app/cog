package tasks_test

import (
	"net/url"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/tasks"
)

func davURL(env *cogtest.Env, name string) string {
	return env.Providers.Config.BaseURL + "/remote.php/dav/calendars/alice/" + name
}

func put(t *testing.T, env *cogtest.Env, u *cogtest.User, name string, lines ...string) {
	t.Helper()
	body := strings.Join(append(append([]string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Test//EN"}, lines...), "END:VCALENDAR"), "\r\n") + "\r\n"
	req := cogtest.NewRequest(t, "PUT", davURL(env, name), strings.NewReader(body))
	req.Header.Set("Content-Type", "text/calendar; charset=utf-8")
	if code := env.Do(t, req, u.Auth).StatusCode; code != 201 && code != 204 {
		t.Fatalf("PUT %s: %d", name, code)
	}
}

func get(t *testing.T, env *cogtest.Env, u *cogtest.User, name string) string {
	t.Helper()
	return env.Do(t, cogtest.NewRequest(t, "GET", davURL(env, name), nil), u.Auth).Status(200).ReadBody()
}

// personalTask returns the task with uid in alice's personal list.
func personalTask(t *testing.T, tp *tasks.Plugin, alice *cogtest.User, uid string) (*tasks.TaskList, *tasks.Task) {
	t.Helper()
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)
	lists, err := tp.Lists(alice.ID)
	require.NoError(t, err)
	task, err := tasks.GetTaskByUID(db, lists[0].ID, uid)
	require.NoError(t, err)
	return &lists[0], task
}

// appleTodo is a reminder as macOS Reminders writes it, with a vendor
// alarm, a sort order and properties Cog does not model.
var appleTodo = []string{
	"BEGIN:VTODO",
	"STATUS:NEEDS-ACTION",
	"CREATED:20131112T111120Z",
	"UID:apple-1",
	"DUE;VALUE=DATE:20140912",
	"X-APPLE-SORT-ORDER:430306542",
	"SUMMARY:Stundenaufstellung",
	"PRIORITY:1",
	"DTSTAMP:20140813T103551Z",
	"SEQUENCE:15",
	"LOCATION;ALTREP=\"http://example.com/room\":Room 1",
	"X-MOZ-LASTACK:20140813T103551Z",
	"GEO:37.386013;-122.082932",
	"BEGIN:VALARM",
	"X-WR-ALARMUID:F84333D4",
	"TRIGGER:PT8H",
	"ATTACH;VALUE=URI:Basso",
	"ACTION:AUDIO",
	"END:VALARM",
	"END:VTODO",
}

func TestDAVTaskKeepsClientData(t *testing.T) {
	env, tp, alice := setupDB(t)
	put(t, env, alice, "personal/apple-1.ics", appleTodo...)

	body := get(t, env, alice, "personal/apple-1.ics")
	for _, want := range []string{
		"DUE;VALUE=DATE:20140912", "X-APPLE-SORT-ORDER:430306542", "SEQUENCE:15", "GEO:37.386013;-122.082932",
		`LOCATION;ALTREP="http://example.com/room":Room 1`, "X-WR-ALARMUID:F84333D4", "ATTACH;VALUE=URI:Basso",
	} {
		assert.Contains(t, body, want)
	}

	list, task := personalTask(t, tp, alice, "apple-1")
	require.Equal(t, "Stundenaufstellung", task.Summary)
	require.True(t, task.DueIsDate && task.DueAt != nil)
	require.Equal(t, 430306542, *task.SortOrder)

	// A web edit of the summary changes only that, and the bookkeeping.
	cookie, csrf := sessionAuth(t, env, alice)
	form := url.Values{
		"summary": {"Hours"}, "description": {""}, "priority": {"1"}, "status": {"NEEDS-ACTION"},
		"percent_complete": {"0"}, "list_id": {itoa(list.ID)}, "due_at_date": {"2014-09-12T00:00"},
	}
	env.PostForm(t, "/apps/tasks/task/"+itoa(task.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)
	body = get(t, env, alice, "personal/apple-1.ics")
	for _, want := range []string{
		"SUMMARY:Hours", "DUE;VALUE=DATE:20140912", "X-APPLE-SORT-ORDER:430306542", "SEQUENCE:15",
		"X-MOZ-LASTACK", "GEO:", "X-WR-ALARMUID:F84333D4", "LAST-MODIFIED:",
	} {
		assert.Contains(t, body, want)
	}
	assert.NotContains(t, body, "DTSTAMP:20140813T103551Z")

	// Moving the due date bumps SEQUENCE and drops the snooze state that
	// referred to the old one.
	form.Set("due_at_date", "2014-09-13T10:00")
	env.PostForm(t, "/apps/tasks/task/"+itoa(task.ID), form, cookie, csrf, cogtest.StopRedirect).Status(303)
	body = get(t, env, alice, "personal/apple-1.ics")
	assert.Contains(t, body, "SEQUENCE:16")
	assert.Contains(t, body, "DUE:20140913T100000Z") // the list is in UTC
	assert.NotContains(t, body, "X-MOZ-LASTACK")
	assert.Contains(t, body, "X-WR-ALARMUID:F84333D4")
}

func TestParentMayArriveLater(t *testing.T) {
	env, tp, alice := setupDB(t)
	put(t, env, alice, "personal/child.ics", "BEGIN:VTODO", "UID:child", "SUMMARY:Child", "RELATED-TO;RELTYPE=PARENT:parent", "END:VTODO")

	list, child := personalTask(t, tp, alice, "child")
	require.Equal(t, "parent", child.ParentUID)
	require.True(t, child.ParentID == nil, "the parent is not here yet")

	put(t, env, alice, "personal/parent.ics", "BEGIN:VTODO", "UID:parent", "SUMMARY:Parent", "END:VTODO")
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)
	all, err := tasks.ListTasks(db, list.ID)
	require.NoError(t, err)
	roots := tasks.RootTasks(all)
	require.Equal(t, 1, len(roots))
	require.Equal(t, "Parent", roots[0].Summary)
	require.Equal(t, 1, len(tasks.ChildrenOf(all, roots[0].ID)))

	// RELATED-TO without RELTYPE is a parent too (RFC 5545 §3.2.15), and
	// tasks that name each other cannot hide from the tree.
	put(t, env, alice, "personal/parent.ics", "BEGIN:VTODO", "UID:parent", "SUMMARY:Parent", "RELATED-TO:child", "END:VTODO")
	all, err = tasks.ListTasks(db, list.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(tasks.RootTasks(all)))
}

func TestCompleteRecurringTask(t *testing.T) {
	env, tp, alice := setupDB(t)
	put(t, env, alice, "personal/weekly.ics", "BEGIN:VTODO", "UID:weekly", "SUMMARY:Bins", "STATUS:NEEDS-ACTION",
		"DTSTART;TZID=Europe/Berlin:20261001T090000", "DUE;TZID=Europe/Berlin:20261001T100000",
		"RRULE:FREQ=WEEKLY;COUNT=3", "EXDATE;TZID=Europe/Berlin:20261008T090000", "END:VTODO")
	list, task := personalTask(t, tp, alice, "weekly")
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)

	// Completing it moves the series on, past the excluded week, and it
	// stays open.
	task, err = tasks.ToggleTask(db, list, task, env.Now())
	require.NoError(t, err)
	require.Equal(t, "NEEDS-ACTION", task.Status)
	body := get(t, env, alice, "personal/weekly.ics")
	assert.Contains(t, body, "DTSTART;TZID=Europe/Berlin:20261015T090000")
	assert.Contains(t, body, "DUE;TZID=Europe/Berlin:20261015T100000")
	assert.Contains(t, body, "COUNT=1")
	assert.NotContains(t, body, "EXDATE")
	assert.Contains(t, body, "SEQUENCE:1")

	// The last occurrence completes the task.
	task, err = tasks.ToggleTask(db, list, task, env.Now())
	require.NoError(t, err)
	require.True(t, task.Done())
	body = get(t, env, alice, "personal/weekly.ics")
	assert.Contains(t, body, "STATUS:COMPLETED")
	assert.Contains(t, body, "COMPLETED:")
	assert.Contains(t, body, "PERCENT-COMPLETE:100")
}

func TestJournalRoundTrip(t *testing.T) {
	env, tp, alice := setupDB(t)
	mk := cogtest.NewRequest(t, "MKCALENDAR", davURL(env, "notes/"), strings.NewReader(`<C:mkcalendar xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav">
<D:set><D:prop><C:supported-calendar-component-set><C:comp name="VTODO"/><C:comp name="VJOURNAL"/></C:supported-calendar-component-set></D:prop></D:set></C:mkcalendar>`))
	env.Do(t, mk, alice.Auth).Status(201)
	put(t, env, alice, "notes/j.ics", "BEGIN:VJOURNAL", "UID:j", "SUMMARY:Day one", "DTSTART;VALUE=DATE:20261025",
		"DESCRIPTION:First", "DESCRIPTION:Second", "RELATED-TO:apple-1", "X-JTX-COLOR:-1", "END:VJOURNAL")
	body := get(t, env, alice, "notes/j.ics")
	for _, want := range []string{"BEGIN:VJOURNAL", "SUMMARY:Day one", "DESCRIPTION:First", "DESCRIPTION:Second", "RELATED-TO:apple-1", "X-JTX-COLOR:-1"} {
		assert.Contains(t, body, want)
	}
	// Journal entries are not tasks.
	db, err := tp.DB(alice.Username)
	require.NoError(t, err)
	lists, err := tp.Lists(alice.ID)
	require.NoError(t, err)
	for _, l := range lists {
		all, err := tasks.ListTasks(db, l.ID)
		require.NoError(t, err)
		require.Equal(t, 0, len(all))
	}
}
