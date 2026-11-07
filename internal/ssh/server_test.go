package ssh

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/require"

	gossh "golang.org/x/crypto/ssh"
)

// testEnv sets up a full Cog environment with an SSH server on a random port.
type testEnv struct {
	providers *services.Providers
	keys      *keys.Store
	sshAddr   string
	sshSrv    *Server
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	sysDB, err := sql.Open("sqlite3", "file:"+filepath.Join(t.TempDir(), "system.db")+"?_busy_timeout=5000&_journal_mode=WAL&_foreign_keys=on")
	require.NoError(t, err)
	require.NoError(t, db.Migrate(sysDB))
	t.Cleanup(func() { sysDB.Close() })

	dataDir := t.TempDir()
	cfg := config.Config{
		DataDir:               dataDir,
		SSHEnabled:            true,
		SSHAddr:               "127.0.0.1:0",
		DisableProcessPlugins: true,
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := services.NewFromDB(log, cfg, sysDB, clock.System())
	require.NoError(t, err)
	// Tests choose short passwords and run offline.
	require.NoError(t, p.Settings.SetMany(map[string]string{settings.KeyPasswordMinLength: "1", settings.KeyPasswordBreachCheck: "false"}))
	t.Cleanup(p.Plugins.Stop)
	if err := p.Plugins.Start(context.Background()); err != nil {
		t.Fatalf("start plugins: %v", err)
	}

	ks, err := keys.NewStore(p.DB, p.Plugins, p.Users, clock.System())
	require.NoError(t, err)
	sshSrv, err := New(cfg, p, ks, log)
	require.NoError(t, err)

	// Listen on random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	sshSrv.listener = ln
	addr := ln.Addr().String()

	go sshSrv.ListenAndServe()
	t.Cleanup(func() { sshSrv.Close() })

	return &testEnv{
		providers: p,
		keys:      ks,
		sshAddr:   addr,
		sshSrv:    sshSrv,
	}
}

func (e *testEnv) createUser(t *testing.T, username, password string) {
	t.Helper()
	_, err := e.providers.Users.Create(username, password)
	require.NoError(t, err)
	require.NoError(t, e.providers.Plugins.EnsureUserDirs(username))
}

func (e *testEnv) sshClient(t *testing.T, username, password string) *gossh.Client {
	t.Helper()
	client, err := gossh.Dial("tcp", e.sshAddr, &gossh.ClientConfig{
		User:            username,
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHPasswordAuth(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice", "secret")

	// Good password should connect.
	client := env.sshClient(t, "alice", "secret")
	sess, err := client.NewSession()
	require.NoError(t, err)
	sess.Close()

	// Bad password should fail.
	_, err = gossh.Dial("tcp", env.sshAddr, &gossh.ClientConfig{
		User:            "alice",
		Auth:            []gossh.AuthMethod{gossh.Password("wrong")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	if err == nil {
		t.Fatal("expected auth failure with wrong password")
	}
}

func TestSSHExec(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "bob", "pass")

	// Write a test file.
	filesDir := env.providers.Plugins.UserFilesDir("bob")
	os.WriteFile(filepath.Join(filesDir, "test.txt"), []byte("hello ssh\n"), 0644)

	client := env.sshClient(t, "bob", "pass")
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	out, err := sess.Output("cat test.txt")
	require.NoError(t, err)
	if string(out) != "hello ssh\n" {
		t.Fatalf("got %q, want %q", out, "hello ssh\n")
	}
}

func TestSSHSCPUpload(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "carol", "pass")

	client := env.sshClient(t, "carol", "pass")

	// Use scp sink protocol to upload a file.
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)

	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)

	err = sess.Start("scp -t /upload.txt")
	require.NoError(t, err)

	// Read initial ready byte.
	ack := make([]byte, 1)
	_, err = io.ReadFull(stdout, ack)
	require.NoError(t, err)

	content := "scp test data"
	header := fmt.Sprintf("C0644 %d upload.txt\n", len(content))
	stdin.Write([]byte(header))
	_, err = io.ReadFull(stdout, ack) // ready for data
	require.NoError(t, err)
	stdin.Write([]byte(content))
	stdin.Write([]byte{0})            // trailing NUL
	_, err = io.ReadFull(stdout, ack) // done
	require.NoError(t, err)
	stdin.Close()
	sess.Wait()

	// Verify file was created.
	filesDir := env.providers.Plugins.UserFilesDir("carol")
	data, err := os.ReadFile(filepath.Join(filesDir, "upload.txt"))
	require.NoError(t, err)
	if string(data) != content {
		t.Fatalf("got %q, want %q", data, content)
	}
}

func TestSSHSFTP(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "dave", "pass")

	// Write a test file.
	filesDir := env.providers.Plugins.UserFilesDir("dave")
	os.WriteFile(filepath.Join(filesDir, "sftp-test.txt"), []byte("sftp content"), 0644)

	client := env.sshClient(t, "dave", "pass")

	// Open SFTP subsystem.
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	// Request sftp subsystem and verify it's accepted.
	ok, err := sess.SendRequest("subsystem", true, gossh.Marshal(struct{ S string }{S: "sftp"}))
	require.NoError(t, err)
	if !ok {
		t.Fatal("sftp subsystem rejected")
	}
}

func TestSSHPublicKeyAuth(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "eve", "pass")

	// Generate a test key pair.
	signer, err := gossh.ParsePrivateKey(testEd25519Key)
	require.NoError(t, err)

	// Add the public key.
	pubKey := gossh.MarshalAuthorizedKey(signer.PublicKey())
	_, err = env.keys.Add("eve", "test-key", string(pubKey))
	require.NoError(t, err)

	// Connect with public key.
	client, err := gossh.Dial("tcp", env.sshAddr, &gossh.ClientConfig{
		User:            "eve",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(signer)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	defer client.Close()

	sess, err := client.NewSession()
	require.NoError(t, err)
	out, err := sess.Output("echo hello")
	require.NoError(t, err)
	if string(out) != "hello\n" {
		t.Fatalf("got %q, want %q", out, "hello\n")
	}
	sess.Close()
}

// testEd25519Key is a pre-generated ed25519 private key for tests.
var testEd25519Key = []byte(`-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACA7kgtSiXq0hrfCkAzEuvLYA48It5cUnJ/9bTKL1BL1ZgAAAJBs5eGDbOXh
gwAAAAtzc2gtZWQyNTUxOQAAACA7kgtSiXq0hrfCkAzEuvLYA48It5cUnJ/9bTKL1BL1Zg
AAAEA31Tqum/LQegI1/0KeqdjnbBvmiQrgzyruZzhOegd8wzuSC1KJerSGt8KQDMS68tgD
jwi3lxScn/1tMovUEvVmAAAADXRlc3RAY29ndGVzdHM=
-----END OPENSSH PRIVATE KEY-----`)
