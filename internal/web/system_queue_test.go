package web_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestSystemIndexDrainsFromQueue proves the system database goes through the
// change queue, not a direct hand-off from the update hook: the nudge is the
// only thing the hook does, so disabling the sweep and relying on the nudge
// still has to produce a queued-then-drained change.
func TestSystemIndexDrainsFromQueue(t *testing.T) {
	old := search.DrainInterval
	search.DrainInterval = 10 * time.Minute
	t.Cleanup(func() { search.DrainInterval = old })

	env := cogtest.New(t)
	env.CreateUser(t, "hortensia", "pass")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := env.Providers.Search.SearchSystem("hortensia", 10, 0)
		require.NoError(t, err)
		if res != nil && len(res.Hits) > 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("user never reached the system index via the queue")
}
