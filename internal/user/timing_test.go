package user_test

import (
	"errors"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/password"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
	"golang.org/x/crypto/bcrypt"
)

// TestAuthenticate_TimingForUnknownUserApproximatesWrongPassword pins the
// audit security.md #25 fix: Authenticate runs bcrypt against a dummy hash
// when the user doesn't exist, so an attacker can't distinguish "no such
// user" from "wrong password" via response time.
//
// We don't require exact equality (CI nondeterminism), but the unknown-user
// path must be in the same order of magnitude as wrong-password — both
// dominated by a single bcrypt invocation (~80ms at DefaultCost).
func TestAuthenticate_TimingForUnknownUserApproximatesWrongPassword(t *testing.T) {
	// cogtest lowers the cost to bcrypt's minimum, where a comparison is
	// too quick to time against the database lookup.
	cost := password.Cost
	password.Cost = bcrypt.DefaultCost
	t.Cleanup(func() { password.Cost = cost })
	env := cogtest.New(t)
	_, err := env.Providers.Users.Create("alice", "hunter2")
	require.NoError(t, err)

	const trials = 5
	avg := func(f func()) time.Duration {
		var total time.Duration
		for range trials {
			start := time.Now()
			f()
			total += time.Since(start)
		}
		return total / trials
	}

	wrongPw := avg(func() {
		_, err := env.Providers.Users.Authenticate("alice", "wrong")
		require.True(t, errors.Is(err, user.ErrAuthFailed))
	})
	unknownUser := avg(func() {
		_, err := env.Providers.Users.Authenticate("nope", "wrong")
		require.True(t, errors.Is(err, user.ErrAuthFailed))
	})

	// Both should be at least bcrypt cost (10ms-ish minimum). Hard ratio
	// floor at 3x: pre-fix the unknown-user path returned in microseconds.
	if wrongPw < 5*time.Millisecond {
		t.Skipf("wrongPw too fast (%v) — unreliable host", wrongPw)
	}
	ratio := float64(wrongPw) / float64(unknownUser)
	if ratio > 3 || ratio < 0.33 {
		t.Fatalf("timing diverges: wrongPw=%v unknown=%v ratio=%.2f (pre-fix this was ~100x)",
			wrongPw, unknownUser, ratio)
	}
}

func TestGetByEmail_Match(t *testing.T) {
	env := cogtest.New(t)
	alice, _ := env.Providers.Users.Create("alice", "pass")
	em := "alice@example.com"
	require.NoError(t, env.Providers.Users.Update(alice.ID, user.UpdateOpts{Email: &em}))

	got, err := env.Providers.Users.GetByEmail(em)
	require.NoError(t, err)
	require.Equal(t, alice.ID, got.ID)
}

func TestGetByEmail_Empty(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.GetByEmail("")
	require.True(t, errors.Is(err, user.ErrNotFound))
}

func TestGetByEmail_Unknown(t *testing.T) {
	env := cogtest.New(t)
	_, err := env.Providers.Users.GetByEmail("ghost@example.com")
	require.True(t, errors.Is(err, user.ErrNotFound))
}
