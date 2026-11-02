package auth_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/auth"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// TestWebAuthn_BeginRegistration_StoresEncryptedSession asserts that
// BeginRegistration writes the active challenge into webauthn_sessions and
// that the stored blob is NOT the raw JSON — the audit's "session_data
// plaintext" finding is fixed.
func TestWebAuthn_BeginRegistration_StoresEncryptedSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	require.NotNil(t, env.Providers.WebAuthn)

	options, err := env.Providers.WebAuthn.BeginRegistration(u.ID, u.Username, u.Username)
	require.NoError(t, err)
	require.True(t, len(options) > 0)

	// One session row.
	var n int
	require.NoError(t, env.Providers.DB.QueryRow(
		`SELECT COUNT(*) FROM webauthn_sessions WHERE user_id = ?`, u.ID,
	).Scan(&n))
	require.Equal(t, 1, n)

	// Stored session_data is base64 (encryptSecret output) and does NOT
	// contain JSON keys like "challenge" — i.e. it was encrypted.
	var stored string
	require.NoError(t, env.Providers.DB.QueryRow(
		`SELECT session_data FROM webauthn_sessions WHERE user_id = ?`, u.ID,
	).Scan(&stored))
	require.True(t, !strings.Contains(stored, "challenge"))
	require.True(t, !strings.Contains(stored, "user_id"))
	// Sanity: response options DID include a base64-decoded challenge value.
	require.True(t, strings.Contains(string(options), "challenge"))
}

func TestWebAuthn_BeginRegistration_ReplacesPriorSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, err := env.Providers.WebAuthn.BeginRegistration(u.ID, u.Username, u.Username)
	require.NoError(t, err)
	_, err = env.Providers.WebAuthn.BeginRegistration(u.ID, u.Username, u.Username)
	require.NoError(t, err)

	var n int
	require.NoError(t, env.Providers.DB.QueryRow(
		`SELECT COUNT(*) FROM webauthn_sessions WHERE user_id = ?`, u.ID,
	).Scan(&n))
	require.Equal(t, 1, n)
}

func TestWebAuthn_CredentialCRUD(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")

	// Empty list.
	creds, err := env.Providers.WebAuthn.ListCredentials(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 0, len(creds))

	count, err := env.Providers.WebAuthn.CredentialCount(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 0, count)

	// Insert two credentials directly (we can't drive full WebAuthn ceremony
	// in tests without a fake authenticator).
	insertCred(t, env, "alice-key-1", alice.ID, "yubikey-blue")
	insertCred(t, env, "alice-key-2", alice.ID, "yubikey-green")
	insertCred(t, env, "bob-key-1", bob.ID, "phone")

	creds, err = env.Providers.WebAuthn.ListCredentials(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 2, len(creds))

	count, err = env.Providers.WebAuthn.CredentialCount(alice.ID)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	// Bob has only his own.
	creds, _ = env.Providers.WebAuthn.ListCredentials(bob.ID)
	require.Equal(t, 1, len(creds))

	// Delete alice's first credential.
	require.NoError(t, env.Providers.WebAuthn.DeleteCredential("alice-key-1", alice.ID))
	count, _ = env.Providers.WebAuthn.CredentialCount(alice.ID)
	require.Equal(t, 1, count)

	// Bob cannot delete Alice's credential by ID alone (scoped by user).
	require.True(t, errors.Is(env.Providers.WebAuthn.DeleteCredential("alice-key-2", bob.ID), auth.ErrCredentialNotFound))
	count, _ = env.Providers.WebAuthn.CredentialCount(alice.ID)
	require.Equal(t, 1, count) // unchanged: Bob's delete didn't touch Alice's
}

func TestWebAuthn_BeginLogin_NoCredentials(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, err := env.Providers.WebAuthn.BeginLogin(u.ID, u.Username, u.Username)
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "no webauthn credentials"))
}

// insertCred manually inserts a minimal credential row for tests that only
// need to exercise the read/delete paths.
func insertCred(t *testing.T, env *cogtest.Env, id, userID, name string) {
	t.Helper()
	// A minimal valid credential JSON. The library doesn't strictly validate
	// fields on unmarshal — they just round-trip.
	data, _ := json.Marshal(map[string]any{
		"ID":              []byte("rawcredid-" + id),
		"PublicKey":       []byte{1, 2, 3},
		"AttestationType": "none",
	})
	_, err := env.Providers.DB.Exec(
		`INSERT INTO webauthn_credentials (id, user_id, name, credential_data, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, userID, name, string(data), time.Now().Unix(),
	)
	require.NoError(t, err)
}

// A finish with no ceremony in progress is the client's failure; a store
// that cannot be read is the server's. Callers answer 403 for one and 500
// for the other, so the two must be told apart.
func TestWebAuthn_FinishErrorsAreClassified(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	wa := env.Providers.WebAuthn

	err := wa.FinishLogin(u.ID, u.Username, u.Username, []byte("{}"))
	require.True(t, errors.Is(err, auth.ErrWebAuthnRejected))

	_, err = wa.BeginRegistration(u.ID, u.Username, u.Username)
	require.NoError(t, err)
	err = wa.FinishRegistration(u.ID, u.Username, u.Username, "key", []byte("not json"))
	require.True(t, errors.Is(err, auth.ErrWebAuthnRejected))

	_, err = env.Providers.DB.Exec(`ALTER TABLE webauthn_sessions RENAME TO webauthn_sessions_gone`)
	require.NoError(t, err)
	err = wa.FinishLogin(u.ID, u.Username, u.Username, []byte("{}"))
	require.True(t, err != nil)
	require.True(t, !errors.Is(err, auth.ErrWebAuthnRejected))
}

func TestWebAuthn_DeleteUnknownCredential(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	err := env.Providers.WebAuthn.DeleteCredential("nope", u.ID)
	require.True(t, errors.Is(err, auth.ErrCredentialNotFound))
}
