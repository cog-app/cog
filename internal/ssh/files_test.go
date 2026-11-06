package ssh

import (
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin/files"

	"github.com/pkg/sftp"
	gossh "golang.org/x/crypto/ssh"
)

func (e *testEnv) files(t *testing.T) *files.Plugin {
	t.Helper()
	plug, ok := e.providers.Plugins.Plugin(files.PluginID)
	require.True(t, ok)
	fp, ok := plug.(*files.Plugin)
	require.True(t, ok)
	return fp
}

// setQuota limits username to limit bytes on the default volume.
func (e *testEnv) setQuota(t *testing.T, username string, limit int64) {
	t.Helper()
	u, err := e.providers.Users.GetByUsername(username)
	require.NoError(t, err)
	vol, err := e.providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	require.NoError(t, e.providers.Quotas.Set(vol.ID, "USER", u.ID, limit))
}

// scpSend sends one file over the SCP sink protocol to target, and returns
// what the server said if it refused.
func scpSend(t *testing.T, client *gossh.Client, target, name, content string) error {
	t.Helper()
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)
	stderr, err := sess.StderrPipe()
	require.NoError(t, err)
	require.NoError(t, sess.Start("scp -t "+target))

	ack := make([]byte, 1)
	steps := []func() error{
		func() error { _, err := io.ReadFull(stdout, ack); return err }, // ready
		func() error { _, err := fmt.Fprintf(stdin, "C0644 %d %s\n", len(content), name); return err },
		func() error { _, err := io.ReadFull(stdout, ack); return err }, // ready for data
		func() error { _, err := io.WriteString(stdin, content+"\x00"); return err },
		func() error { _, err := io.ReadFull(stdout, ack); return err }, // done
	}
	for _, step := range steps {
		if err := step(); err != nil {
			msg, rerr := io.ReadAll(stderr)
			require.NoError(t, rerr)
			return errors.New(string(msg))
		}
	}
	require.NoError(t, stdin.Close())
	return sess.Wait()
}

// TestSSHSCPRecorded: a file received over SCP has its row, and one over
// quota is refused before anything is written.
func TestSSHSCPRecorded(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "carol", "pass")
	env.setQuota(t, "carol", 10)
	client := env.sshClient(t, "carol", "pass")
	fp := env.files(t)

	require.NoError(t, scpSend(t, client, "/files", "a.txt", "12345"))
	meta, err := fp.GetByPath("carol", "/a.txt")
	require.NoError(t, err)
	require.Equal(t, int64(5), meta.Size)

	err = scpSend(t, client, ".", "b.txt", "123456")
	require.True(t, err != nil, "an upload over quota was accepted")
	_, err = fp.GetByPath("carol", "/b.txt")
	require.True(t, errors.Is(err, files.ErrNotFound), "an over-quota upload left a row: %v", err)
}

// TestSSHSCPNameEscape: a name the client sends must stay in the target
// directory; ".." would otherwise reach the session's virtual root.
func TestSSHSCPNameEscape(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "carol", "pass")
	client := env.sshClient(t, "carol", "pass")

	for _, name := range []string{"..", "../.ssh/authorized_keys", "x/y"} {
		err := scpSend(t, client, "/files", name, "ssh-ed25519 AAAA")
		require.True(t, err != nil, "name %q was accepted", name)
	}
	keys, err := env.keys.List("carol")
	require.NoError(t, err)
	require.Equal(t, 0, len(keys))
}

// TestSSHSFTPRecorded: SFTP writes are held to the quota as they are made
// and recorded when the file is closed.
func TestSSHSFTPRecorded(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "dave", "pass")
	env.setQuota(t, "dave", 10)
	client, err := sftp.NewClient(env.sshClient(t, "dave", "pass"))
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	fp := env.files(t)

	f, err := client.OpenFile("/files/a.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	require.NoError(t, err)
	_, err = f.Write([]byte("12345678"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	meta, err := fp.GetByPath("dave", "/a.txt")
	require.NoError(t, err)
	require.Equal(t, int64(8), meta.Size)

	f, err = client.OpenFile("/files/b.txt", os.O_CREATE|os.O_WRONLY)
	require.NoError(t, err)
	_, err = f.Write([]byte("12345"))
	require.True(t, err != nil, "a write over quota was accepted")
	require.NoError(t, f.Close())

	require.NoError(t, client.Rename("/files/a.txt", "/files/c.txt"))
	moved, err := fp.GetByPath("dave", "/c.txt")
	require.NoError(t, err)
	require.Equal(t, meta.ID, moved.ID)

	require.NoError(t, client.Remove("/files/c.txt"))
	_, err = fp.GetByPath("dave", "/c.txt")
	require.True(t, errors.Is(err, files.ErrNotFound), "a removed file kept its row: %v", err)
}
