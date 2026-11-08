package ssh

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gokrazy/rsync"
	"github.com/gokrazy/rsync/rsyncclient"
	gossh "golang.org/x/crypto/ssh"

	"git.duckfam.us/jj-software/cog/internal/testing/assert"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// rsyncOverSSH runs the gokrazy/rsync client against the server over an SSH
// session, as "rsync -e ssh" does: the client starts "rsync --server ..." on
// the server and speaks the protocol over the session's stdin and stdout.
// push sends local to remote; otherwise remote is fetched into local.
// serverArgs are added to the server's command line, for options the
// gokrazy/rsync client does not pass on as tridge rsync's does (--delete).
func rsyncOverSSH(t *testing.T, client *gossh.Client, args []string, push bool, remote, local string, serverArgs ...string) (string, error) {
	t.Helper()
	opts := []rsyncclient.Option{rsyncclient.DontRestrict(), rsyncclient.WithStderr(nopWriteCloser{io.Discard})}
	if push {
		opts = append(opts, rsyncclient.WithSender())
	}
	cl, err := rsyncclient.New(args, opts...)
	require.NoError(t, err)

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)
	var stderr strings.Builder
	sess.Stderr = &stderr

	// The rsync client escapes what the shell would split or expand.
	serverOpts := cl.ServerCommandOptions(remote)
	var cmd []string
	for _, a := range append(serverOpts[:1:1], append(serverArgs, serverOpts[1:]...)...) {
		cmd = append(cmd, strings.ReplaceAll(a, " ", `\ `))
	}
	require.NoError(t, sess.Start("rsync "+strings.Join(cmd, " ")))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, runErr := cl.Run(ctx, &rsync.BothCloser{ReadCloser: io.NopCloser(stdout), WriteCloser: stdin}, []string{local})
	waitErr := sess.Wait()
	return stderr.String(), errors.Join(runErr, waitErr)
}

func writeLocal(t *testing.T, dir string, files map[string]string, mtime time.Time) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		require.NoError(t, os.Chtimes(p, mtime, mtime))
	}
}

// readCog reads a user's file through the files plugin.
func readCog(t *testing.T, fp *files.Plugin, username, p string) string {
	t.Helper()
	fsys, err := fp.FS(username)
	require.NoError(t, err)
	b, err := fsys.ReadFile(p)
	require.NoError(t, err)
	return string(b)
}

func TestSSHRsyncPush(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "erin", "pass")
	client := env.sshClient(t, "erin", "pass")
	fp := env.files(t)

	src := t.TempDir()
	mtime := time.Date(2025, time.March, 1, 12, 0, 0, 0, time.UTC)
	writeLocal(t, src, map[string]string{
		"a.txt":          "alpha",
		"sub/b.txt":      "bravo",
		"with space.txt": "spaced",
	}, mtime)

	out, err := rsyncOverSSH(t, client, []string{"-a"}, true, "backup/", src+"/")
	require.NoError(t, err, out)

	assert.Equal(t, "alpha", readCog(t, fp, "erin", "/backup/a.txt"))
	assert.Equal(t, "bravo", readCog(t, fp, "erin", "/backup/sub/b.txt"))
	assert.Equal(t, "spaced", readCog(t, fp, "erin", "/backup/with space.txt"))

	// Each file is recorded, with the sender's modification time, which
	// is what lets the next run skip it.
	meta, err := fp.GetByPath("erin", "/backup/a.txt")
	require.NoError(t, err)
	assert.Equal(t, int64(5), meta.Size)
	assert.Equal(t, mtime.Unix(), meta.MTime)

	// No temporary file is left beside the ones received.
	fsys, err := fp.FS("erin")
	require.NoError(t, err)
	list, err := fsys.List("/backup")
	require.NoError(t, err)
	for _, fi := range list {
		assert.False(t, strings.HasPrefix(fi.Name, "."), "left behind: %s", fi.Name)
	}

	// A changed file is sent again against the copy already there.
	writeLocal(t, src, map[string]string{"a.txt": "alpha and omega"}, mtime.Add(time.Hour))
	out, err = rsyncOverSSH(t, client, []string{"-a"}, true, "backup/", src+"/")
	require.NoError(t, err, out)
	assert.Equal(t, "alpha and omega", readCog(t, fp, "erin", "/backup/a.txt"))

	// It is an update of the file, as a PUT is: its ID, and so its shares
	// and comments, stay.
	updated, err := fp.GetByPath("erin", "/backup/a.txt")
	require.NoError(t, err)
	assert.Equal(t, meta.ID, updated.ID)
}

// TestRsyncRootDelete covers what --delete does to the root: walk its fs.FS
// for files the sender lacks and remove them. (The gokrazy/rsync client does
// not tell the server to delete, so this is not driven over SSH.)
func TestRsyncRootDelete(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "frank", "pass")
	fp := env.files(t)
	userFS, err := SessionFS(env.providers, env.keys, "frank")
	require.NoError(t, err)
	fsys, err := fp.FS("frank")
	require.NoError(t, err)
	require.NoError(t, fsys.Mkdir("/mirror"))
	require.NoError(t, fsys.Mkdir("/mirror/old"))
	require.NoError(t, fsys.Put("/mirror/keep.txt", strings.NewReader("k")))
	require.NoError(t, fsys.Put("/mirror/old/gone.txt", strings.NewReader("g")))

	root, err := newRsyncRoot(userFS, "/files")
	require.NoError(t, err)
	sub, err := root.Sub("mirror")
	require.NoError(t, err)

	var walked []string
	require.NoError(t, fs.WalkDir(sub.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, p)
		if p == "old" {
			require.NoError(t, sub.RemoveAll(p))
			return fs.SkipDir
		}
		return nil
	}))
	assert.Equal(t, ". keep.txt old", strings.Join(walked, " "))

	_, err = fp.GetByPath("frank", "/mirror/old/gone.txt")
	assert.True(t, errors.Is(err, files.ErrNotFound), "deleted file still recorded: %v", err)
	assert.Equal(t, "k", readCog(t, fp, "frank", "/mirror/keep.txt"))

	// What is missing is reported as os.IsNotExist, which the receiver
	// relies on; removing it again is not an error.
	_, err = sub.Lstat("old")
	assert.True(t, os.IsNotExist(err), "Lstat of a removed directory: %v", err)
	_, err = sub.Open("old/gone.txt")
	assert.True(t, os.IsNotExist(err), "Open of a removed file: %v", err)
	assert.NoError(t, sub.RemoveAll("old"))

	// Names that climb out of the root are refused.
	for _, name := range []string{"../keep.txt", "/etc/passwd", "a/../../b"} {
		_, err := sub.Lstat(name)
		assert.True(t, err != nil, "%q was accepted", name)
	}
	assert.True(t, root.Remove(".") != nil, "the root itself was removed")
}

func TestSSHRsyncPull(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "gina", "pass")
	client := env.sshClient(t, "gina", "pass")
	fp := env.files(t)
	fsys, err := fp.FS("gina")
	require.NoError(t, err)
	require.NoError(t, fsys.Mkdir("/photos"))
	require.NoError(t, fsys.Mkdir("/photos/2025"))
	require.NoError(t, fsys.Put("/photos/2025/beach.jpg", strings.NewReader("sand")))
	require.NoError(t, fsys.Put("/photos/cover.jpg", strings.NewReader("front")))

	dest := t.TempDir()
	out, err := rsyncOverSSH(t, client, []string{"-a"}, false, "photos/", dest+"/")
	require.NoError(t, err, out)

	got, err := os.ReadFile(filepath.Join(dest, "2025", "beach.jpg"))
	require.NoError(t, err)
	assert.Equal(t, "sand", string(got))
	got, err = os.ReadFile(filepath.Join(dest, "cover.jpg"))
	require.NoError(t, err)
	assert.Equal(t, "front", string(got))
}

func TestSSHRsyncQuota(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "hal", "pass")
	env.setQuota(t, "hal", 10)
	client := env.sshClient(t, "hal", "pass")
	fp := env.files(t)

	src := t.TempDir()
	writeLocal(t, src, map[string]string{"big.bin": strings.Repeat("x", 64)}, time.Now())
	_, err := rsyncOverSSH(t, client, []string{"-a"}, true, "./", src+"/")
	require.True(t, err != nil, "an upload over quota was accepted")

	_, err = fp.GetByPath("hal", "/big.bin")
	assert.True(t, errors.Is(err, files.ErrNotFound), "an over-quota upload left a row: %v", err)
}

// TestSSHRsyncStaysInFiles: a destination that climbs out of /files lands
// inside it, so rsync cannot reach the session's other entries, such as the
// authorized keys.
func TestSSHRsyncStaysInFiles(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "ivy", "pass")
	client := env.sshClient(t, "ivy", "pass")

	src := t.TempDir()
	writeLocal(t, src, map[string]string{"authorized_keys": "ssh-ed25519 AAAA"}, time.Now())
	out, err := rsyncOverSSH(t, client, []string{"-a"}, true, "../.ssh/", src+"/")
	require.NoError(t, err, out)

	keys, err := env.keys.List("ivy")
	require.NoError(t, err)
	assert.Equal(t, 0, len(keys))
	assert.Equal(t, "ssh-ed25519 AAAA", readCog(t, env.files(t), "ivy", "/.ssh/authorized_keys"))
}

func TestRsyncServerArgs(t *testing.T) {
	for _, tc := range []struct {
		command string
		want    string
	}{
		{"rsync --server -logDtpre.iLsfxC . backup/", "--server -logDtpre.iLsfxC . backup/"},
		{"rsync --server --sender -logDtpre.iLsfxC . /files/photos/", "--server --sender -logDtpre.iLsfxC . photos/"},
		{"rsync --server -e.LsfxC . ../../etc", "--server -e.LsfxC . etc"},
		{`rsync --server -e.LsfxC . my\ docs/`, "--server -e.LsfxC . my docs/"},
		{"rsync --server -e.LsfxC . /files", "--server -e.LsfxC . ./"},
		{"rsync --server -e.LsfxC . ~", "--server -e.LsfxC . ./"},
	} {
		got, err := rsyncServerArgs(tc.command)
		require.NoError(t, err)
		assert.Equal(t, tc.want, strings.Join(got, " "), tc.command)
	}
	for _, bad := range []string{"rsync --daemon", "rsync", "rsync --server -e.LsfxC", "rsync --server 'unterminated"} {
		_, err := rsyncServerArgs(bad)
		assert.True(t, err != nil, "%q was accepted", bad)
	}
}
