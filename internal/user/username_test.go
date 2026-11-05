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
	} {
		_, err := users.Create(name, "pass")
		require.True(t, errors.Is(err, user.ErrInvalidUsername), "%q: got %v", name, err)
	}
}

func TestCreateAcceptsOrdinaryUsernames(t *testing.T) {
	env := cogtest.New(t)
	for _, name := range []string{"alice", "Bob", "a.b-c_d+e", "someone@example.com", "7", strings.Repeat("a", 64)} {
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
