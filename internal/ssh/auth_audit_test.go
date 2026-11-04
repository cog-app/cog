package ssh

import (
	"net"
	"testing"
	"time"

	"github.com/pquerna/otp/totp"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/ratelimit"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// A user with MFA cannot log in over SSH with just their password.
func TestPasswordCallback_RefusesPasswordWithMFA(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	alice, err := env.providers.Users.GetByUsername("alice")
	require.NoError(t, err)
	key, err := env.providers.TOTP.BeginSetup(alice.ID, "alice", "Cog")
	require.NoError(t, err)
	code, err := totp.GenerateCode(key.Secret(), time.Now())
	require.NoError(t, err)
	_, err = env.providers.TOTP.ConfirmSetup(alice.ID, code)
	require.NoError(t, err)

	ac := &authCallbacks{users: env.providers.Users, appPasswords: env.providers.AppPasswords, keys: env.keys}
	_, err = ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte("secret"))
	require.True(t, err != nil, "password alone opened an MFA account")

	appPW, _, err := env.providers.AppPasswords.Create(alice.ID, "ssh")
	require.NoError(t, err)
	_, err = ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte(appPW))
	require.NoError(t, err)
}

// Password guessing over SSH is throttled, even for the right password once
// the limit is hit.
func TestPasswordCallback_RateLimited(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	ac := &authCallbacks{
		users: env.providers.Users, appPasswords: env.providers.AppPasswords, keys: env.keys,
		limiter: ratelimit.New(3, time.Minute, clock.System()),
	}
	conn := &stubConnMeta{user: "alice", remoteAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 2222}}
	for range 3 {
		_, err := ac.passwordCallback(conn, []byte("wrong"))
		require.True(t, err != nil)
	}
	_, err := ac.passwordCallback(conn, []byte("secret"))
	require.True(t, err == errRateLimited, "got %v", err)
}
