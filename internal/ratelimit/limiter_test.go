package ratelimit_test

import (
	"path/filepath"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestLimiter: both stores limit a key after MaxAttempts in the window,
// forget attempts that leave it, keep keys apart, and reset one key.
func TestLimiter(t *testing.T) {
	sys, err := db.OpenSystem(filepath.Join(t.TempDir(), "system.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sys.Close() })

	for name, make := range map[string]func(clock.Clock) *ratelimit.Limiter{
		"memory": func(c clock.Clock) *ratelimit.Limiter { return ratelimit.New(3, time.Minute, c) },
		"shared": func(c clock.Clock) *ratelimit.Limiter { return ratelimit.NewShared(sys, "test", 3, time.Minute, c) },
	} {
		t.Run(name, func(t *testing.T) {
			tc := clock.NewTest(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
			l := make(tc.Clock())
			for range 3 {
				require.False(t, l.IsLimited("a"), "limited early")
				l.RecordFailure("a")
			}
			require.True(t, l.IsLimited("a"), "not limited after 3 failures")
			require.False(t, l.IsLimited("b"), "keys share a count")

			tc.Advance(61 * time.Second)
			require.False(t, l.IsLimited("a"), "attempts outlived the window")

			l.RecordFailure("a")
			l.RecordFailure("a")
			l.RecordFailure("a")
			l.Reset("a")
			require.False(t, l.IsLimited("a"), "reset did not clear the key")
		})
	}
}

// TestSharedLimiterOutlivesRestart: a limiter on the system database counts
// what an earlier one recorded, as a restarted server or another replica
// would, and limiters of different names do not.
func TestSharedLimiterOutlivesRestart(t *testing.T) {
	sys, err := db.OpenSystem(filepath.Join(t.TempDir(), "system.db"))
	require.NoError(t, err)
	t.Cleanup(func() { sys.Close() })

	tc := clock.NewTest(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	before := ratelimit.NewShared(sys, "auth", 2, time.Minute, tc.Clock())
	before.RecordFailure("1.2.3.4")
	before.RecordFailure("1.2.3.4")

	after := ratelimit.NewShared(sys, "auth", 2, time.Minute, tc.Clock())
	require.True(t, after.IsLimited("1.2.3.4"), "a restart forgave the guesser")
	other := ratelimit.NewShared(sys, "invitation-mail", 2, time.Minute, tc.Clock())
	require.False(t, other.IsLimited("1.2.3.4"), "limiters share a count")
}
