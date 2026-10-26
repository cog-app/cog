package calendar_test

import (
	"database/sql"
	"testing"

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
	ev, err := calendar.SaveEvent(db, cal, nil, ics, env.Now())
	require.NoError(t, err)
	return ev
}
