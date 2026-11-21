package ssh

import (
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/user"
)

// An admin can su to another user and work in their files; exit comes back.
func TestSSHSuAdmin(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "admin", "pass") // the first user is the admin
	env.createUser(t, "bob", "pass")
	require.NoError(t, os.WriteFile(filepath.Join(env.providers.Plugins.UserFilesDir("bob"), "bob.txt"), []byte("bob's\n"), 0o644))

	sess, err := env.sshClient(t, "admin", "pass").NewSession()
	require.NoError(t, err)
	defer sess.Close()
	out, err := sess.Output("su bob; whoami; cat bob.txt; exit; whoami")
	require.NoError(t, err)
	require.Equal(t, "bob\nbob's\nadmin\n", string(out))
}

// Anyone else has no su at all.
func TestSSHSuNotForUsers(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "admin", "pass")
	env.createUser(t, "bob", "pass")

	sess, err := env.sshClient(t, "bob", "pass").NewSession()
	require.NoError(t, err)
	defer sess.Close()
	out, err := sess.CombinedOutput("su admin; help")
	require.NoError(t, err) // help's status
	require.Contains(t, string(out), "su: command not found")
	require.NotContains(t, string(out), "Act as another user")
}

// An admin who is no longer one cannot su, though their session began
// when they were.
func TestSSHSuAfterDemotion(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "admin", "pass")
	env.createUser(t, "bob", "pass")
	admin, err := env.providers.Users.GetByUsername("admin")
	require.NoError(t, err)

	su := switchUser(env.providers, env.keys, env.sshSrv.log, admin.ID)
	_, _, err = su("bob")
	require.NoError(t, err)
	_, _, err = su("nobody")
	require.Contains(t, err.Error(), "does not exist")

	no := false
	require.NoError(t, env.providers.Users.Update(admin.ID, user.UpdateOpts{IsAdmin: &no}))
	_, _, err = su("bob")
	require.Contains(t, err.Error(), "permission denied")
}

// In an interactive session, Ctrl-D leaves an su rather than the session.
func TestSSHSuInteractiveCtrlD(t *testing.T) {
	client := startTestServer(t) // testuser, the first user, is the admin
	err := shellSession(t, client, "su testuser", "\x04exit 5")
	require.Equal(t, 5, exitStatusOf(t, err))
}
