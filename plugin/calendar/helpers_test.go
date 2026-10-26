package calendar_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/dav/ical"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// calEnv sets up a user with their default calendar and returns the plugin,
// the user's database and that calendar.
func calEnv(t *testing.T) (*cogtest.Env, *cogtest.User, *calendar.Plugin, *sql.DB, *calendar.Calendar) {
	t.Helper()
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")
	plug, _ := env.Providers.Plugins.Plugin("calendar")
	cp := plug.(*calendar.Plugin)
	db, err := cp.DB(alice.Username)
	require.NoError(t, err)
	cals, err := cp.Calendars(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(cals))
	return env, alice, cp, db, &cals[0]
}

// saveICS stores an ICS in cal as Cog would.
func saveICS(t *testing.T, env *cogtest.Env, db *sql.DB, cal *calendar.Calendar, ics string) *calendar.Event {
	t.Helper()
	ev, err := calendar.SaveICS(db, cal, ics, env.Now())
	require.NoError(t, err)
	return ev
}

// genICS writes a minimal single event, as a client might PUT it.
func genICS(uid, summary, description, location string, start, end time.Time, allDay bool, stamp time.Time) string {
	var b strings.Builder
	b.WriteString("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\n")
	if allDay {
		b.WriteString("DTSTART;VALUE=DATE:" + start.Format("20060102") + "\r\nDTEND;VALUE=DATE:" + end.Format("20060102") + "\r\n")
	} else {
		b.WriteString("DTSTART:" + start.UTC().Format("20060102T150405Z") + "\r\nDTEND:" + end.UTC().Format("20060102T150405Z") + "\r\n")
	}
	for _, p := range [][2]string{{"SUMMARY", summary}, {"DESCRIPTION", description}, {"LOCATION", location}} {
		if p[1] != "" {
			b.WriteString(p[0] + ":" + ical.EscapeText(p[1]) + "\r\n")
		}
	}
	b.WriteString("DTSTAMP:" + stamp.UTC().Format("20060102T150405Z") + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n")
	return b.String()
}

// rendered returns the iCalendar text Cog serves for an event.
func rendered(t *testing.T, ev *calendar.Event) string {
	t.Helper()
	s, err := calendar.RenderEvent(ev)
	require.NoError(t, err)
	return s
}
