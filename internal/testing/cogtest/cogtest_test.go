package cogtest_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/group"
	"git.duckfam.us/jj-software/cog/internal/search"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestConcurrentWritesWhileDraining pins the system database the harness gives
// a test to one that behaves like the server's.
//
// Every write fires a change-capture trigger that inserts into the change
// queue, and the search drainer deletes from that same queue on its own
// goroutine. Two connections therefore contend for one table on every write.
// On a file database with WAL and a busy timeout the loser waits; on a
// shared-cache in-memory database it gets SQLITE_LOCKED ("database table is
// locked") immediately, because SQLite does not run the busy handler for
// shared-cache table locks.
func TestConcurrentWritesWhileDraining(t *testing.T) {
	// Drain constantly so the queue is locked as often as possible.
	oldInterval := search.DrainInterval
	search.DrainInterval = time.Millisecond
	t.Cleanup(func() { search.DrainInterval = oldInterval })

	env := cogtest.New(t)
	users := make([]string, 4)
	for i := range users {
		users[i] = env.CreateUser(t, fmt.Sprintf("user%d", i), "pass").ID
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i, userID := range users {
		wg.Go(func() {
			for n := range 12 {
				grp, err := env.Providers.Groups.Create(fmt.Sprintf("team-%d-%d", i, n), nil, nil)
				if err == nil {
					err = env.Providers.Groups.AddMember(grp.ID, userID, group.User)
				}
				if err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
		})
	}
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
}
