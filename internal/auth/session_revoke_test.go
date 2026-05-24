package auth_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestDeleteAllForUser_KillsEverySession verifies the logout-everywhere flow
// removes every session for the target user without touching other users'.
func TestDeleteAllForUser_KillsEverySession(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	// Three sessions for alice, one for bob.
	a1, _ := env.Providers.Sessions.Create(alice.ID)
	a2, _ := env.Providers.Sessions.Create(alice.ID)
	a3, _ := env.Providers.Sessions.Create(alice.ID)
	b1, _ := env.Providers.Sessions.Create(bob.ID)

	require.NoError(t, env.Providers.Sessions.DeleteAllForUser(alice.ID))

	// All of alice's sessions are dead.
	for _, s := range []*struct{ ID string }{{a1.ID}, {a2.ID}, {a3.ID}} {
		got, err := env.Providers.Sessions.Validate(s.ID)
		require.NoError(t, err)
		require.Nil(t, got)
	}

	// Bob's session unaffected.
	got, err := env.Providers.Sessions.Validate(b1.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
}

// TestDeleteAllForUser_NoSessions is a no-op (must not error).
func TestDeleteAllForUser_NoSessions(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	require.NoError(t, env.Providers.Sessions.DeleteAllForUser(alice.ID))
}

// TestDeleteAllExcept_KeepsCurrentSession verifies the "log out other
// browsers" flow keeps the present session and revokes the rest.
func TestDeleteAllExcept_KeepsCurrentSession(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	keep, _ := env.Providers.Sessions.Create(alice.ID)
	other1, _ := env.Providers.Sessions.Create(alice.ID)
	other2, _ := env.Providers.Sessions.Create(alice.ID)
	bobSess, _ := env.Providers.Sessions.Create(bob.ID)

	require.NoError(t, env.Providers.Sessions.DeleteAllExcept(alice.ID, keep.ID))

	// keep survives.
	got, err := env.Providers.Sessions.Validate(keep.ID)
	require.NoError(t, err)
	require.NotNil(t, got)

	// Other Alice sessions are gone.
	for _, id := range []string{other1.ID, other2.ID} {
		got, _ := env.Providers.Sessions.Validate(id)
		require.Nil(t, got)
	}

	// Bob unaffected.
	got, _ = env.Providers.Sessions.Validate(bobSess.ID)
	require.NotNil(t, got)
}

// TestDeleteAllExcept_BogusExceptionDeletesAll: if the "except" ID doesn't
// match any session, every session is deleted (audit-relevant when a stale
// session-ID is passed by mistake).
func TestDeleteAllExcept_BogusExceptionDeletesAll(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	a, _ := env.Providers.Sessions.Create(alice.ID)
	b, _ := env.Providers.Sessions.Create(alice.ID)

	require.NoError(t, env.Providers.Sessions.DeleteAllExcept(alice.ID, "no-such-session"))

	got1, _ := env.Providers.Sessions.Validate(a.ID)
	got2, _ := env.Providers.Sessions.Validate(b.ID)
	require.Nil(t, got1)
	require.Nil(t, got2)
}
