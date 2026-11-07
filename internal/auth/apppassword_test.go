package auth_test

import (
	"errors"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/db"
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

// TestAppPasswordTokenFormat checks a token is "<id>.<secret>", the id is the
// row id, and only a hash of the secret is stored.
func TestAppPasswordTokenFormat(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	token, ap, err := env.Providers.AppPasswords.Create(u.ID, "app")
	require.NoError(t, err)
	id, secret, ok := strings.Cut(token, ".")
	require.True(t, ok, "token %q has no separator", token)
	require.Equal(t, ap.ID, id)
	require.Equal(t, 64, len(secret))
	require.False(t, strings.Contains(token, ":"), "a ':' would break Basic auth")
	require.False(t, strings.Contains(ap.TokenHash, secret))
}

func TestAppPasswordRejections(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	aps := env.Providers.AppPasswords

	aliceTok, _, err := aps.Create(alice.ID, "a")
	require.NoError(t, err)
	bobTok, _, err := aps.Create(bob.ID, "b")
	require.NoError(t, err)
	aliceID, aliceSecret, _ := strings.Cut(aliceTok, ".")
	_, bobSecret, _ := strings.Cut(bobTok, ".")

	for name, tok := range map[string]string{
		"wrong secret":          aliceID + "." + strings.Repeat("0", 64),
		"another user's secret": aliceID + "." + bobSecret,
		"unknown id":            strings.Repeat("f", 32) + "." + aliceSecret,
		"no separator":          aliceID + aliceSecret,
		"empty id":              "." + aliceSecret,
		"empty secret":          aliceID + ".",
		"empty":                 "",
	} {
		_, err := aps.Validate(tok)
		require.True(t, errors.Is(err, auth.ErrInvalidAppPassword), "%s: got %v", name, err)
	}
}

// TestAppPasswordOldFormatRejected checks that a token from before ids were
// added (64 hex characters, bcrypt hashed) no longer validates, and that the
// migration removes its row.
func TestAppPasswordOldFormatRejected(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	old := strings.Repeat("ab", 32)
	// A bcrypt-shaped hash: any "$2" value marks a row from the old scheme.
	_, err := env.Providers.DB.Exec(
		`INSERT INTO app_passwords (id, user_id, name, token_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
		"oldid", u.ID, "legacy", "$2a$04$abcdefghijklmnopqrstuuJ0b7ZQ8sWb9w0eYcK6Yy1rQ9Yp2m0aG", 1700000000,
	)
	require.NoError(t, err)
	kept, _, err := env.Providers.AppPasswords.Create(u.ID, "new")
	require.NoError(t, err)

	_, err = env.Providers.AppPasswords.Validate(old)
	require.True(t, errors.Is(err, auth.ErrInvalidAppPassword), "got %v", err)

	require.NoError(t, db.Migrate(env.Providers.DB))
	list, err := env.Providers.AppPasswords.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(list))
	require.Equal(t, "new", list[0].Name)
	uid, err := env.Providers.AppPasswords.Validate(kept)
	require.NoError(t, err)
	require.Equal(t, u.ID, uid)
}

// Every app password has the form LooksLikeAppPassword accepts, and nothing
// a person types does.
func TestLooksLikeAppPassword(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	for range 5 {
		token, _, err := env.Providers.AppPasswords.Create(alice.ID, "x")
		require.NoError(t, err)
		require.True(t, auth.LooksLikeAppPassword(token), "%q", token)
		require.False(t, auth.LooksLikeAppPassword(strings.ToUpper(token)))
		require.False(t, auth.LooksLikeAppPassword(token[:len(token)-1]))
		require.False(t, auth.LooksLikeAppPassword(token+"0"))
	}
	for _, s := range []string{"", "pass", "correct horse battery staple", ".", "abc.def", strings.Repeat("a", 97)} {
		require.False(t, auth.LooksLikeAppPassword(s), "%q", s)
	}
}
