package calendar_test

import (
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/calendar"
)

// D22: calendar objects count toward storage, recounted from what is
// stored, so a deletion is reflected at once.
func TestCalendarObjectsCountTowardStorage(t *testing.T) {
	env := cogtest.New(t, calendar.New())
	alice := env.CreateUser(t, "alice", "pass")
	before, err := env.Providers.StorageUsage("alice")
	require.NoError(t, err)

	path := env.Providers.Config.BaseURL + "/remote.php/dav/calendars/alice/personal/series.ics"
	env.Do(t, cogtest.NewRequest(t, "PUT", path, strings.NewReader(seriesICS)), alice.Auth).Status(201)

	after, err := env.Providers.StorageUsage("alice")
	require.NoError(t, err)
	require.True(t, after > before, "usage %d did not grow from %d", after, before)
	inDAV, err := env.Providers.DAV.Usage("alice")
	require.NoError(t, err)
	require.Equal(t, after-before, inDAV)

	env.Do(t, cogtest.NewRequest(t, "DELETE", path, nil), alice.Auth).Status(204)
	gone, err := env.Providers.StorageUsage("alice")
	require.NoError(t, err)
	require.Equal(t, before, gone)
}
