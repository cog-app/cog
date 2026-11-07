package user_test

import (
	"errors"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

func TestCreateRejectsUnsafeUsernames(t *testing.T) {
	env := cogtest.New(t)
	users := env.Providers.Users
	for _, name := range []string{
		"", "z/../victim", "../victim", "..", ".", ".hidden", "a\\b", "a b", "a\x00b", "-flag",
		strings.Repeat("a", 65),
		"app_data", "App_Data", "avatars", "system.db", "cog-chunk-123",
		"files", "dashboard", // plugin IDs
		"alice+bot", // AgentSeparator marks an agent
	} {
		_, err := users.Create(name, "pass")
		require.True(t, errors.Is(err, user.ErrInvalidUsername), "%q: got %v", name, err)
	}
}

func TestCreateAcceptsOrdinaryUsernames(t *testing.T) {
	env := cogtest.New(t)
	for _, name := range []string{"alice", "Bob", "a.b-c_d", "someone@example.com", "7", strings.Repeat("a", 64)} {
		_, err := env.Providers.Users.Create(name, "pass")
		require.NoError(t, err, name)
	}
}

func TestCreateRejectsNameDifferingOnlyInCase(t *testing.T) {
	env := cogtest.New(t)
	env.CreateUser(t, "alice", "pass")
	_, err := env.Providers.Users.Create("Alice", "pass")
	require.True(t, errors.Is(err, user.ErrUsernameTaken), "got %v", err)
}

func TestOnlyFirstUserIsAdmin(t *testing.T) {
	env := cogtest.New(t)
	first, err := env.Providers.Users.Create("first", "pass")
	require.NoError(t, err)
	second, err := env.Providers.Users.Create("second", "pass")
	require.NoError(t, err)
	require.True(t, first.IsAdmin, "first user should be admin")
	require.False(t, second.IsAdmin, "second user should not be admin")
}

// An agent's username is its owner's, AgentSeparator, and its own name, so
// it says whose it is and no one else can take it.
func TestAgentUsernameNamesItsOwner(t *testing.T) {
	env := cogtest.New(t)
	users := env.Providers.Users
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	a, err := users.CreateAgent(alice.ID, "helper")
	require.NoError(t, err)
	require.Equal(t, "alice+helper", a.Username)
	b, err := users.CreateAgent(bob.ID, "helper")
	require.NoError(t, err)
	require.Equal(t, "bob+helper", b.Username)

	// The full name is taken as the short one; another owner's prefix is not.
	_, err = users.CreateAgent(alice.ID, "alice+helper")
	require.True(t, errors.Is(err, user.ErrUsernameTaken), "got %v", err)
	_, err = users.CreateAgent(alice.ID, "bob+x")
	require.True(t, errors.Is(err, user.ErrInvalidUsername), "got %v", err)

	for _, name := range []string{"", "-x", "a/b", "a b", "x+y", strings.Repeat("a", 64-len("alice+")+1)} {
		_, err := users.CreateAgent(alice.ID, name)
		require.True(t, errors.Is(err, user.ErrInvalidUsername), "%q: got %v", name, err)
	}
	_, err = users.CreateAgent(alice.ID, strings.Repeat("a", 64-len("alice+")))
	require.NoError(t, err)

	// An agent cannot own one, so a name never nests.
	_, err = users.CreateAgent(a.ID, "sub")
	require.True(t, errors.Is(err, user.ErrNotAgentOwner), "got %v", err)

	bad, err := users.Invalid()
	require.NoError(t, err)
	require.Equal(t, 0, len(bad), "%v", bad)
}
