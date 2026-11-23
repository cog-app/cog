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
		guard: &ratelimit.Guard{Limiter: ratelimit.New(3, time.Minute, clock.System())},
	}
	conn := &stubConnMeta{user: "alice", remoteAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 2222}}
	for _, guess := range []string{"wrong1", "wrong2", "wrong3"} {
		_, err := ac.passwordCallback(conn, []byte(guess))
		require.True(t, err != nil)
	}
	_, err := ac.passwordCallback(conn, []byte("secret"))
	require.True(t, err == ratelimit.ErrLimited, "got %v", err)
}

// The guard refuses a repeated wrong password without checking it, and the
// callback must not take the check's untouched results for a success: the
// SSH library treats nil permissions with a nil error as signed in.
func TestPasswordCallback_RepeatedWrongPasswordRefused(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	ac := &authCallbacks{
		users: env.providers.Users, appPasswords: env.providers.AppPasswords, keys: env.keys,
		guard: &ratelimit.Guard{Limiter: ratelimit.New(3, time.Minute, clock.System())},
	}
	conn := &stubConnMeta{user: "alice", remoteAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 2222}}
	for range 5 {
		perms, err := ac.passwordCallback(conn, []byte("wrong"))
		require.True(t, err != nil && perms == nil, "repeat let in: %v %v", perms, err)
	}
	perms, err := ac.passwordCallback(conn, []byte("secret"))
	require.NoError(t, err)
	require.True(t, perms != nil)
}

// A client blocked for wrong passwords may still sign in with an app
// password, which cannot be guessed; its password is refused.
func TestPasswordCallback_AppPasswordWhileLimited(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	alice, err := env.providers.Users.GetByUsername("alice")
	require.NoError(t, err)
	appPW, _, err := env.providers.AppPasswords.Create(alice.ID, "ssh")
	require.NoError(t, err)
	ac := &authCallbacks{
		users: env.providers.Users, appPasswords: env.providers.AppPasswords, keys: env.keys,
		guard: &ratelimit.Guard{Limiter: ratelimit.New(3, time.Minute, clock.System())},
	}
	conn := &stubConnMeta{user: "alice", remoteAddr: &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 2222}}
	for _, guess := range []string{"wrong1", "wrong2", "wrong3"} {
		_, err := ac.passwordCallback(conn, []byte(guess))
		require.True(t, err != nil)
	}
	_, err = ac.passwordCallback(conn, []byte("secret"))
	require.True(t, err == ratelimit.ErrLimited, "got %v", err)
	perms, err := ac.passwordCallback(conn, []byte(appPW))
	require.NoError(t, err)
	require.Equal(t, alice.ID, perms.Extensions["user_id"])
}

// With account passwords refused (setup's choice for a server reachable
// from the internet), only an app password signs in.
func TestPasswordCallback_NoAccountPasswords(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")
	alice, err := env.providers.Users.GetByUsername("alice")
	require.NoError(t, err)
	ac := &authCallbacks{users: env.providers.Users, appPasswords: env.providers.AppPasswords, keys: env.keys, noAccountPasswords: true}
	perms, err := ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte("secret"))
	require.True(t, err != nil && perms == nil, "the account password signed in")

	appPW, _, err := env.providers.AppPasswords.Create(alice.ID, "ssh")
	require.NoError(t, err)
	_, err = ac.passwordCallback(&stubConnMeta{user: "alice"}, []byte(appPW))
	require.NoError(t, err)
}
