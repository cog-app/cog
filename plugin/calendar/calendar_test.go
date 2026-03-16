package calendar_test

import (
	"testing"
	"time"

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
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	// Seed an event directly via the store.
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	calendar.EnsureDefaultCalendar(db)
	cals, err := calendar.ListCalendars(db)
	require.NoError(t, err)
	require.True(t, len(cals) > 0)

	now := time.Now()
	dtstart := now.Add(1 * time.Hour)
	dtend := now.Add(2 * time.Hour)

	ics := calendar.GenerateICS("test-uid-1", "Team Meeting", "Weekly sync", "Room 42",
		dtstart, dtend, false, time.Now())
	fields, err := calendar.ParseVEvent(ics)
	require.NoError(t, err)
	_, err = calendar.UpsertEvent(db, cals[0].ID, "test-uid-1", ics, fields)
	require.NoError(t, err)

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
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")

	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)

	_, err = calendar.CreateCalendar(db, "work-cal", "Work", "#ff0000", "Work events")
	require.NoError(t, err)

	cals, err := calendar.ListCalendars(db)
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
