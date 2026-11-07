package ratelimit_test

import (
	"errors"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func newGuard(max int) *ratelimit.Guard {
	return &ratelimit.Guard{Limiter: ratelimit.New(max, time.Hour, clock.System()), Delay: time.Millisecond}
}

func pass() (bool, error) { return true, nil }
func fail() (bool, error) { return false, nil }

// A client's success against one account does not clear its failures
// against others (audit H7): guessing interleaved with good logins to an
// account of its own still runs out.
func TestGuardSuccessDoesNotClearClient(t *testing.T) {
	g := newGuard(3)
	for range 3 {
		_, err := g.Try("203.0.113.1", ratelimit.AccountKey("victim"), "", nil, fail)
		require.NoError(t, err)
		ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("mine"), "", nil, pass)
		if err != nil {
			require.Equal(t, ratelimit.ErrLimited, err)
			return
		}
		require.True(t, ok)
	}
	_, err := g.Try("203.0.113.1", ratelimit.AccountKey("victim"), "", nil, pass)
	require.Equal(t, ratelimit.ErrLimited, err)
}

// A slowed account queues at most MaxWaiting attempts; the next is refused
// at once rather than parked in a goroutine of its own.
func TestGuardSlowedAccountCapsWaiters(t *testing.T) {
	g := &ratelimit.Guard{Limiter: ratelimit.New(3, time.Hour, clock.System()), Delay: 300 * time.Millisecond}
	for i := range 5 {
		_, err := g.Try("198.51.100."+string(rune('1'+i)), ratelimit.AccountKey("alice"), "", nil, fail)
		require.NoError(t, err)
	}

	// One attempt more than the queue holds, all at once. Every goroutine
	// reaches the queue long before the first admitted one, which sleeps
	// Delay, lets the next in, so exactly one is refused.
	n := ratelimit.MaxWaiting + 1
	done := make(chan error, n)
	for i := range n {
		go func() {
			_, err := g.Try("192.0.2."+string(rune('1'+i)), ratelimit.AccountKey("alice"), "", nil, fail)
			done <- err
		}()
	}
	refused := 0
	for range n {
		if err := <-done; err != nil {
			require.Equal(t, ratelimit.ErrLimited, err)
			refused++
		}
	}
	require.Equal(t, 1, refused)
}

// Failures against an account from many addresses slow it, but its owner
// can still sign in: guessing cannot lock them out.
func TestGuardAccountIsSlowedNotLocked(t *testing.T) {
	g := newGuard(3)
	for i := range 5 {
		_, err := g.Try("198.51.100."+string(rune('1'+i)), ratelimit.AccountKey("alice"), "", nil, fail)
		require.NoError(t, err)
	}
	ok, err := g.Try("192.0.2.7", ratelimit.AccountKey("Alice"), "", nil, pass)
	require.NoError(t, err)
	require.True(t, ok)
}

// An app retrying a revoked token or an old password sends the same wrong
// secret over and over. Only its first refusal counts: its owner, behind
// the same address, can still sign in, and the repeat is not even checked.
func TestGuardRepeatedSecretCountsOnce(t *testing.T) {
	g := newGuard(3)
	checks := 0
	stale := func() (bool, error) { checks++; return false, nil }
	for range 50 {
		ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "old-token", nil, stale)
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.Equal(t, 1, checks)
	ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "right", nil, pass)
	require.NoError(t, err)
	require.True(t, ok)
}

// Varying the secret is guessing, and every guess counts.
func TestGuardDistinctSecretsCount(t *testing.T) {
	g := newGuard(3)
	for i := range 3 {
		_, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "guess"+string(rune('a'+i)), nil, fail)
		require.NoError(t, err)
	}
	_, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "right", nil, pass)
	require.Equal(t, ratelimit.ErrLimited, err)
}

// A repeat is remembered per client and per account: the same wrong
// secret from another address, or at another account, is a new attempt.
func TestGuardRepeatIsPerClientAndAccount(t *testing.T) {
	g := newGuard(10)
	checks := 0
	wrong := func() (bool, error) { checks++; return false, nil }
	for _, c := range []struct{ ip, user string }{
		{"203.0.113.1", "alice"}, {"203.0.113.1", "alice"},
		{"203.0.113.2", "alice"}, {"203.0.113.1", "bob"},
	} {
		_, err := g.Try(c.ip, ratelimit.AccountKey(c.user), "same", nil, wrong)
		require.NoError(t, err)
	}
	require.Equal(t, 3, checks)
}

// A refusal is remembered for the window, then the secret is checked (and
// counted) again.
func TestGuardRepeatExpiresWithWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	g := &ratelimit.Guard{Limiter: ratelimit.New(10, time.Minute, func() time.Time { return now }), Delay: time.Millisecond}
	checks := 0
	wrong := func() (bool, error) { checks++; return false, nil }
	g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "x", nil, wrong)
	g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "x", nil, wrong)
	require.Equal(t, 1, checks)
	now = now.Add(time.Minute)
	g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "x", nil, wrong)
	require.Equal(t, 2, checks)
}

// Forget makes a refused secret worth checking again: the password may
// have just been set to it.
func TestGuardForget(t *testing.T) {
	g := newGuard(10)
	right := false
	check := func() (bool, error) { return right, nil }
	ok, _ := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", nil, check)
	require.False(t, ok)
	right = true
	ok, _ = g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", nil, check)
	require.False(t, ok, "repeat was checked")
	g.Forget(ratelimit.AccountKey("alice"))
	ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", nil, check)
	require.NoError(t, err)
	require.True(t, ok)
}

// A check that cannot tell is neither counted nor remembered: a database
// hiccup must not hold a right password refused.
func TestGuardCheckErrorIsNotARefusal(t *testing.T) {
	g := newGuard(1)
	broken := errors.New("database is locked")
	_, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", nil, func() (bool, error) { return false, broken })
	require.True(t, errors.Is(err, broken), "got %v", err)
	ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", nil, pass)
	require.NoError(t, err)
	require.True(t, ok)
}

// A token is checked first, and even from a limited client: refusing it
// would cut off a working app because something else at its address
// failed. A password from that client is not checked at all.
func TestGuardTokenCheckedWhileLimited(t *testing.T) {
	g := newGuard(3)
	for i := range 3 {
		_, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "guess"+string(rune('a'+i)), nil, fail)
		require.NoError(t, err)
	}
	passwordChecks := 0
	password := func() (bool, error) { passwordChecks++; return true, nil }
	_, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "password", fail, password)
	require.Equal(t, ratelimit.ErrLimited, err)
	require.Equal(t, 0, passwordChecks)

	ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "token", pass, password)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, 0, passwordChecks)
}

// A token's success clears nothing: an owner's apps syncing do not wipe
// the failures of someone guessing the owner's password.
func TestGuardTokenSuccessKeepsAccountFailures(t *testing.T) {
	g := newGuard(3)
	for i := range 3 {
		_, err := g.Try("198.51.100."+string(rune('1'+i)), ratelimit.AccountKey("alice"), "guess", nil, fail)
		require.NoError(t, err)
	}
	ok, err := g.Try("192.0.2.1", ratelimit.AccountKey("alice"), "token", pass, nil)
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, g.Limiter.IsLimited(ratelimit.AccountKey("alice")), "a token cleared the account's failures")
}

// A secret the token check refuses is then checked as a password, so a
// password that happens to look like a token still signs in; and failing
// both counts once.
func TestGuardFallsBackFromTokenToPassword(t *testing.T) {
	g := newGuard(10)
	ok, err := g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "p", fail, pass)
	require.NoError(t, err)
	require.True(t, ok)

	g = newGuard(2)
	_, err = g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "x", fail, fail)
	require.NoError(t, err)
	require.False(t, g.Limited("203.0.113.1"))
	_, err = g.Try("203.0.113.1", ratelimit.AccountKey("alice"), "y", fail, fail)
	require.NoError(t, err)
	require.True(t, g.Limited("203.0.113.1"))
}

// A revoked token is a refusal like any other: counted once, and refused
// unchecked when repeated.
func TestGuardRevokedTokenCountsOnce(t *testing.T) {
	g := newGuard(3)
	checks := 0
	revoked := func() (bool, error) { checks++; return false, nil }
	for range 20 {
		ok, err := g.Try("203.0.113.2", "", "revoked", revoked, nil)
		require.NoError(t, err)
		require.False(t, ok)
	}
	require.Equal(t, 1, checks)
	require.False(t, g.Limited("203.0.113.2"))
}
