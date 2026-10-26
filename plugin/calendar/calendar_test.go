package calendar_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

func TestCalendarPage(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/calendar/", alice.Auth).Status(200)
}

func TestCalendarEventsJSON_Empty(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	now := time.Now()
	start := now.AddDate(0, -1, 0).Format(time.RFC3339)
	end := now.AddDate(0, 1, 0).Format(time.RFC3339)

	var events []map[string]any
	env.Get(t, "/apps/calendar/events.json?start="+start+"&end="+end, alice.Auth).
		Status(200).JSON(&events)
	require.Equal(t, 0, len(events))
}

func TestCalendarEventsJSON_WithEvent(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)

	now := time.Now()
	dtstart := now.Add(1 * time.Hour)
	dtend := now.Add(2 * time.Hour)
	saveICS(t, env, db, cal, calendar.GenerateICS("test-uid-1", "Team Meeting", "Weekly sync", "Room 42",
		dtstart, dtend, false, time.Now()))

	// Fetch via JSON API.
	start := now.AddDate(0, -1, 0).Format(time.RFC3339)
	end := now.AddDate(0, 1, 0).Format(time.RFC3339)

	var events []map[string]any
	env.Get(t, "/apps/calendar/events.json?start="+start+"&end="+end, alice.Auth).
		Status(200).JSON(&events)
	require.Equal(t, 1, len(events))
	require.Equal(t, "Team Meeting", events[0]["title"])
}

func TestCalendarSettings(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	env.Get(t, "/apps/calendar/settings", alice.Auth).Status(200)
}

func TestCalendarCreateCalendar(t *testing.T) {
	_, alice, cp, _, _ := calEnv(t)

	_, err := cp.CreateCalendar(alice.ID, "work-cal", "Work", "#ff0000", "Work events", "")
	require.NoError(t, err)

	cals, err := cp.Calendars(alice.ID)
	require.NoError(t, err)

	found := false
	for _, c := range cals {
		if c.Name == "Work" {
			found = true
			require.Equal(t, "#ff0000", c.Color)
		}
	}
	require.True(t, found)
}

// sessionAuth creates a session for the user and returns HTTPOpt options for
// session cookie and CSRF token, suitable for RegisterAction endpoints.
func sessionAuth(t testing.TB, env *cogtest.Env, userID string) (cogtest.HTTPOpt, cogtest.HTTPOpt) {
	t.Helper()
	sess, err := env.Providers.Sessions.Create(userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	token := auth.CSRFToken(sess.ID, env.Providers.CSRFSecret)
	cookie := cogtest.Header("Cookie", auth.SessionCookieName+"="+sess.ID)
	csrf := cogtest.Header("X-CSRF-Token", token)
	return cookie, csrf
}

func TestCalendarEventDetail(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)

	now := time.Now()
	ev := saveICS(t, env, db, cal, calendar.GenerateICS("detail-uid", "Detail Event", "Details here", "Room 5",
		now.Add(1*time.Hour), now.Add(2*time.Hour), false, now))

	path := fmt.Sprintf("/apps/calendar/events/%d", ev.ID)
	env.Get(t, path, alice.Auth).Status(200).Contains("Detail Event")
}

func TestCalendarCreateUpdateDeleteEvent(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)
	cals := []calendar.Calendar{*cal}

	cookie, csrf := sessionAuth(t, env, alice.ID)

	// Create an event via POST.
	form := url.Values{
		"calendar_id":    {fmt.Sprintf("%d", cals[0].ID)},
		"summary":        {"Created Event"},
		"datetime_start": {"2026-04-01T10:00"},
		"datetime_end":   {"2026-04-01T11:00"},
	}
	env.PostForm(t, "/apps/calendar/events", form, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify the event was created.
	events, err := calendar.ListEvents(db, cals[0].ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(events))
	require.Equal(t, "Created Event", events[0].Summary)
	evID := events[0].ID

	// Update the event via POST.
	updateForm := url.Values{
		"summary":        {"Updated Event"},
		"datetime_start": {"2026-04-01T14:00"},
		"datetime_end":   {"2026-04-01T15:00"},
	}
	env.PostForm(t, fmt.Sprintf("/apps/calendar/events/%d", evID), updateForm, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify the update.
	ev, err := calendar.GetEvent(db, evID)
	require.NoError(t, err)
	require.Equal(t, "Updated Event", ev.Summary)

	// Delete the event via DELETE.
	env.Delete(t, fmt.Sprintf("/apps/calendar/events/%d", evID), cookie, csrf).Status(200)

	// Verify it's gone.
	_, err = calendar.GetEvent(db, evID)
	require.True(t, err != nil)
}

func TestCalendarCreateDeleteCalendarHTTP(t *testing.T) {
	env, alice, cp, _, _ := calEnv(t)

	cookie, csrf := sessionAuth(t, env, alice.ID)

	// Create a calendar via POST.
	form := url.Values{
		"uid":   {"test-cal"},
		"name":  {"Test Calendar"},
		"color": {"#123456"},
	}
	env.PostForm(t, "/apps/calendar/calendars", form, cookie, csrf, cogtest.StopRedirect).Status(303)

	// Verify via store.
	cal, err := cp.CalendarByName(alice.ID, "test-cal")
	require.NoError(t, err)
	require.Equal(t, "Test Calendar", cal.Name)
	require.Equal(t, "#123456", cal.Color)

	// Delete the calendar via DELETE.
	env.Delete(t, fmt.Sprintf("/apps/calendar/calendars/%d", cal.ID), cookie, csrf).Status(200)

	// Verify it's gone.
	_, err = cp.CalendarByName(alice.ID, "test-cal")
	require.True(t, err != nil)
}

func TestCalendarRRulePreview(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Ensure DB is migrated.
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	_, err := cp.DB(alice.Username)
	require.NoError(t, err)

	// Valid RRULE.
	resp := env.Get(t, "/apps/calendar/rrule-preview?rrule=FREQ%3DWEEKLY%3BINTERVAL%3D1", alice.Auth).Status(200)
	resp.Contains("Every week")

	// Empty RRULE returns empty body.
	resp2 := env.Get(t, "/apps/calendar/rrule-preview?rrule=", alice.Auth).Status(200)
	body := resp2.ReadBody()
	require.Equal(t, "", body)

	// Invalid RRULE returns error markup.
	resp3 := env.Get(t, "/apps/calendar/rrule-preview?rrule=INVALID", alice.Auth).Status(200)
	resp3.Contains("Invalid RRULE")
}

func TestCalendarNotifications(t *testing.T) {
	env, alice, _, db, cal := calEnv(t)

	// With no events, notifications should be an empty array.
	var notifs []map[string]any
	env.Get(t, "/apps/calendar/notifications", alice.Auth).Status(200).JSON(&notifs)
	require.Equal(t, 0, len(notifs))

	// Create an event, then manually insert a due notification.
	now := env.Now()
	ev := saveICS(t, env, db, cal, calendar.GenerateICS("notif-uid", "Notify Event", "", "",
		now.Add(1*time.Hour), now.Add(2*time.Hour), false, now))

	// Insert a notification that's already due (trigger_at in the past).
	_, err := db.Exec(`INSERT INTO notifications (event_id, trigger_at) VALUES (?, ?)`,
		ev.ID, now.Unix()-60)
	require.NoError(t, err)

	// Fetch notifications again.
	var notifs2 []map[string]any
	env.Get(t, "/apps/calendar/notifications", alice.Auth).Status(200).JSON(&notifs2)
	require.Equal(t, 1, len(notifs2))
	require.Equal(t, "Notify Event", notifs2[0]["summary"])
}
