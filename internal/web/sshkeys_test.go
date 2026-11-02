package web_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// newSSHKey returns an authorized_keys line for a fresh key, and its
// fingerprint.
func newSSHKey(t *testing.T, comment string) (string, string) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	sshPub, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	line := strings.TrimSpace(string(gossh.MarshalAuthorizedKey(sshPub)))
	if comment != "" {
		line += " " + comment
	}
	return line, gossh.FingerprintSHA256(sshPub)
}

func sshEnv(t *testing.T) *cogtest.Env {
	t.Helper()
	env := cogtest.New(t)
	env.Providers.Config.SSHEnabled = true
	return env
}

func TestSettingsSSHKeys_AddListRemove(t *testing.T) {
	env := sshEnv(t).WithCookieJar(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	line, fp := newSSHKey(t, "alice@laptop")
	env.PostForm(t, "/settings/ssh-keys", url.Values{
		"csrf_token": {token},
		"public_key": {"  " + line + "\n"},
	}, cookie).Status(200).Contains("SSH key added.").Contains(fp).Contains("alice@laptop")

	got, err := env.Providers.SSHKeys.List("alice")
	require.NoError(t, err)
	require.Equal(t, 1, len(got))
	require.Equal(t, "alice@laptop", got[0].Name) // the key's comment

	owner, _, err := env.Providers.SSHKeys.FindByFingerprint(fp)
	require.NoError(t, err)
	require.Equal(t, "alice", owner)

	env.Delete(t, "/settings/ssh-keys/"+got[0].ID, cookie, cogtest.Header("X-CSRF-Token", token)).Status(200)
	_, _, err = env.Providers.SSHKeys.FindByFingerprint(fp)
	require.True(t, errors.Is(err, keys.ErrNotFound))
	env.Get(t, "/settings/security", cookie).Status(200).Contains("No SSH keys yet.")
}

func TestSettingsSSHKeys_Rejected(t *testing.T) {
	env := sshEnv(t).WithCookieJar(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	// Not a key: the message says so and the form keeps what was typed.
	env.PostForm(t, "/settings/ssh-keys", url.Values{
		"csrf_token": {token},
		"name":       {"work"},
		"public_key": {"ssh-ed25519 garbage"},
	}, cookie).Status(200).
		Contains("That is not an SSH public key.").
		Contains("ssh-ed25519 garbage").
		Contains(`value="work"`)

	// Two keys at once are refused rather than the second silently dropped.
	one, _ := newSSHKey(t, "one")
	two, _ := newSSHKey(t, "two")
	env.PostForm(t, "/settings/ssh-keys", url.Values{
		"csrf_token": {token},
		"public_key": {one + "\n" + two},
	}, cookie).Status(200).Contains("That is not an SSH public key.")

	// A key another account has.
	line, _ := newSSHKey(t, "")
	_, err := env.Providers.SSHKeys.Add(bob.Username, "bob's", line)
	require.NoError(t, err)
	env.PostForm(t, "/settings/ssh-keys", url.Values{
		"csrf_token": {token},
		"public_key": {line},
	}, cookie).Status(200).Contains("already been added to an account")

	got, err := env.Providers.SSHKeys.List("alice")
	require.NoError(t, err)
	require.Equal(t, 0, len(got))
}

func TestSettingsSSHKeys_CannotRemoveAnothersKey(t *testing.T) {
	env := sshEnv(t)
	alice := env.CreateUser(t, "alice", "pass")
	bob := env.CreateUser(t, "bob", "pass")
	cookie, token := sessionAndCSRF(t, env, alice.ID)

	line, fp := newSSHKey(t, "bob")
	k, err := env.Providers.SSHKeys.Add(bob.Username, "", line)
	require.NoError(t, err)

	env.Delete(t, "/settings/ssh-keys/"+k.ID, cookie, cogtest.Header("X-CSRF-Token", token)).Status(http.StatusNotFound)
	owner, _, err := env.Providers.SSHKeys.FindByFingerprint(fp)
	require.NoError(t, err)
	require.Equal(t, "bob", owner)
}

func TestSettingsSSHKeys_HiddenWhenSSHOff(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	cookie, _ := sessionAndCSRF(t, env, alice.ID)
	env.Get(t, "/settings/security", cookie).Status(200).NotContains("SSH keys")
}
