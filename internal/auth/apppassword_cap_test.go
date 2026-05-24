package auth_test

import (
	"errors"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestAppPassword_PerUserCap_Enforced verifies that Create refuses to mint
// more than MaxAppPasswordsPerUser tokens. This bounds Validate's O(N)
// bcrypt cost (audit security.md #11).
func TestAppPassword_PerUserCap_Enforced(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	// Fill to the cap.
	for range auth.MaxAppPasswordsPerUser {
		_, _, err := env.Providers.AppPasswords.Create(alice.ID, "tok")
		require.NoError(t, err)
	}

	// One more — refused.
	_, _, err := env.Providers.AppPasswords.Create(alice.ID, "overflow")
	require.True(t, errors.Is(err, auth.ErrTooManyAppPasswords))
}

// TestAppPassword_CapIsPerUser ensures one user hitting the cap doesn't
// block another from creating tokens.
func TestAppPassword_CapIsPerUser(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	for range auth.MaxAppPasswordsPerUser {
		_, _, err := env.Providers.AppPasswords.Create(alice.ID, "tok")
		require.NoError(t, err)
	}

	// Bob can still create.
	_, _, err := env.Providers.AppPasswords.Create(bob.ID, "first")
	require.NoError(t, err)
}

// TestAppPassword_DeleteFreesQuota verifies the user can resume creating
// after deleting an existing token.
func TestAppPassword_DeleteFreesQuota(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")

	var firstID string
	for i := range auth.MaxAppPasswordsPerUser {
		_, ap, err := env.Providers.AppPasswords.Create(alice.ID, "tok")
		require.NoError(t, err)
		if i == 0 {
			firstID = ap.ID
		}
	}

	// At the cap.
	_, _, err := env.Providers.AppPasswords.Create(alice.ID, "blocked")
	require.True(t, errors.Is(err, auth.ErrTooManyAppPasswords))

	// Delete one.
	require.NoError(t, env.Providers.AppPasswords.Delete(firstID, alice.ID))

	// Slot opens.
	_, _, err = env.Providers.AppPasswords.Create(alice.ID, "now-ok")
	require.NoError(t, err)
}
