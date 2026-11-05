package ratelimit_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func newGuard(max int) *ratelimit.Guard {
	return &ratelimit.Guard{Limiter: ratelimit.New(max, time.Hour, clock.System()), Delay: time.Millisecond}
}

func pass() bool { return true }
func fail() bool { return false }

// A client's success against one account does not clear its failures
// against others (audit H7): guessing interleaved with good logins to an
// account of its own still runs out.
func TestGuardSuccessDoesNotClearClient(t *testing.T) {
	g := newGuard(3)
	for range 3 {
		_, err := g.Try("203.0.113.1", ratelimit.AccountKey("victim"), fail)
		require.NoError(t, err)
		ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("mine"), pass)
		if err != nil {
			require.Equal(t, ratelimit.ErrLimited, err)
			return
		}
		require.True(t, ok)
	}
	_, err := g.Try("203.0.113.1", ratelimit.AccountKey("victim"), pass)
	require.Equal(t, ratelimit.ErrLimited, err)
}

// Failures against an account from many addresses slow it, but its owner
// can still sign in: guessing cannot lock them out.
func TestGuardAccountIsSlowedNotLocked(t *testing.T) {
	g := newGuard(3)
	for i := range 5 {
		_, err := g.Try("198.51.100."+string(rune('1'+i)), ratelimit.AccountKey("alice"), fail)
		require.NoError(t, err)
	}
	ok, err := g.Try("192.0.2.7", ratelimit.AccountKey("Alice"), pass)
	require.NoError(t, err)
	require.True(t, ok)
}
