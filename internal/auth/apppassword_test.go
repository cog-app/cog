package auth_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCreateAndValidateAppPassword(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	plaintext, ap, err := env.Providers.AppPasswords.Create(u.ID, "my-app")
	require.NoError(t, err)
	require.NotNil(t, ap)
	require.True(t, len(plaintext) > 0)
	require.Equal(t, "my-app", ap.Name)

	uid, err := env.Providers.AppPasswords.Validate(plaintext)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
}

func TestValidateWrongToken(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, _, err := env.Providers.AppPasswords.Create(u.ID, "my-app")
	require.NoError(t, err)

	_, err = env.Providers.AppPasswords.Validate("totally-wrong-token")
	require.Error(t, err)
}

func TestListByUser(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, _, err := env.Providers.AppPasswords.Create(u.ID, "app1")
	require.NoError(t, err)
	_, _, err = env.Providers.AppPasswords.Create(u.ID, "app2")
	require.NoError(t, err)

	passwords, err := env.Providers.AppPasswords.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 2, len(passwords))
}

func TestDeleteAppPassword(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, ap, err := env.Providers.AppPasswords.Create(u.ID, "app1")
	require.NoError(t, err)

	err = env.Providers.AppPasswords.Delete(ap.ID, u.ID)
	require.NoError(t, err)

	passwords, err := env.Providers.AppPasswords.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(passwords))
}

func TestDeleteOtherUsersAppPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	_, ap, err := env.Providers.AppPasswords.Create(alice.ID, "alice-app")
	require.NoError(t, err)

	// Bob should not be able to delete Alice's app password.
	err = env.Providers.AppPasswords.Delete(ap.ID, bob.ID)
	require.Error(t, err)

	// Alice's password should still exist.
	passwords, err := env.Providers.AppPasswords.ListByUser(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(passwords))
}

func TestMultiplePasswords(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	token1, _, err := env.Providers.AppPasswords.Create(u.ID, "app1")
	require.NoError(t, err)
	token2, _, err := env.Providers.AppPasswords.Create(u.ID, "app2")
	require.NoError(t, err)

	// Both should validate to the same user.
	uid, err := env.Providers.AppPasswords.Validate(token1)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)

	uid, err = env.Providers.AppPasswords.Validate(token2)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
}
