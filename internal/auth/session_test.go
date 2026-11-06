package auth_test

import (
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func TestCreateAndValidateSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)
	require.NotNil(t, sess)
	require.Equal(t, u.ID, sess.UserID)
	require.True(t, len(sess.ID) > 0)

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, sess.ID, got.ID)
	require.Equal(t, u.ID, got.UserID)
}

func TestValidateExpiredSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	// Manually expire.
	_, err = env.Providers.DB.Exec(`UPDATE sessions SET expires_at = 0 WHERE user_id = ?`, u.ID)
	require.NoError(t, err)

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDeleteSession(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Sessions.Delete(sess.ID))

	got, err := env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.Nil(t, got)
}

// TestSessionStoredHashed: the table holds no token, and what it does hold
// does not work as one (audit M1).
func TestSessionStoredHashed(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	sess, err := env.Providers.Sessions.Create(u.ID)
	require.NoError(t, err)

	var stored string
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT id FROM sessions WHERE user_id = ?`, u.ID).Scan(&stored))
	require.True(t, stored != sess.ID, "the table holds the token")
	got, err := env.Providers.Sessions.Validate(stored)
	require.NoError(t, err)
	require.Nil(t, got)

	got, err = env.Providers.Sessions.Validate(sess.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, sess.Handle(), got.Handle())
}

// TestSessionFromBeforeHashing: a row holding its token still signs in, and
// is rewritten to hold the digest.
func TestSessionFromBeforeHashing(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	_, err := env.Providers.DB.Exec(`INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		token, u.ID, env.Now().Unix(), env.Now().Add(time.Hour).Unix())
	require.NoError(t, err)

	listed, err := env.Providers.Sessions.ListByUser(u.ID)
	require.NoError(t, err)
	require.Equal(t, 1, len(listed))

	got, err := env.Providers.Sessions.Validate(token)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, listed[0].Handle(), got.Handle())
	var hashed bool
	require.NoError(t, env.Providers.DB.QueryRow(`SELECT hashed FROM sessions WHERE user_id = ?`, u.ID).Scan(&hashed))
	require.True(t, hashed, "the row was not rewritten")
	got, err = env.Providers.Sessions.Validate(token)
	require.NoError(t, err)
	require.NotNil(t, got)
}
