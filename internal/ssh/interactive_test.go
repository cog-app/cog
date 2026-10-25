package ssh

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/require"

	gossh "golang.org/x/crypto/ssh"
)

func TestSSHInteractiveShell(t *testing.T) {
	dbName := url.PathEscape(t.Name())
	sysDB, err := sql.Open("sqlite3", "file:"+dbName+"?mode=memory&cache=shared&_journal_mode=WAL&_foreign_keys=on")
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
	p := services.NewFromDB(log, cfg, sysDB, clock.System())
	t.Cleanup(p.Plugins.Stop)
	require.NoError(t, p.Plugins.Start(context.Background()))
	_, err = p.Users.Create("testuser", "testpass")
	require.NoError(t, err)
	require.NoError(t, p.Plugins.EnsureUserDirs("testuser"))

	ks := keys.NewStore(p.Plugins, p.Users, clock.System())
	sshSrv, err := New(cfg, p, ks, log)
	require.NoError(t, err)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	sshSrv.listener = ln
	addr := ln.Addr().String()
	go sshSrv.ListenAndServe()
	t.Cleanup(func() { sshSrv.Close() })

	client, err := gossh.Dial("tcp", addr, &gossh.ClientConfig{
		User:            "testuser",
		Auth:            []gossh.AuthMethod{gossh.Password("testpass")},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
	})
	require.NoError(t, err)
	defer client.Close()

	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()

	modes := gossh.TerminalModes{gossh.ECHO: 1}
	err = sess.RequestPty("xterm", 24, 80, modes)
	require.NoError(t, err)

	stdin, err := sess.StdinPipe()
	require.NoError(t, err)

	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)

	err = sess.Shell()
	require.NoError(t, err)

	// Read stdout in a goroutine.
	output := make(chan string, 10)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				output <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// collectUntil reads output chunks until the predicate matches or timeout.
	collectUntil := func(pred func(string) bool, timeout time.Duration, label string) string {
		t.Helper()
		var total strings.Builder
		deadline := time.After(timeout)
		for {
			select {
			case chunk := <-output:
				total.WriteString(chunk)
				if pred(total.String()) {
					return total.String()
				}
			case <-deadline:
				t.Fatalf("Timed out waiting for %s; got: %q", label, total.String())
			}
		}
	}

	// Wait for initial prompt.
	collectUntil(func(s string) bool { return strings.Contains(s, "$ ") }, 5*time.Second, "prompt")

	// Send a command and wait for output + next prompt.
	time.Sleep(100 * time.Millisecond)
	stdin.Write([]byte("echo hello\r"))
	got := collectUntil(func(s string) bool {
		return strings.Contains(s, "$ ") && strings.Count(s, "hello") >= 2
	}, 5*time.Second, "echo output")
	if !strings.Contains(got, "hello") {
		t.Fatalf("Expected echo output; got: %q", got)
	}

	// Send exit and wait for session to close.
	time.Sleep(100 * time.Millisecond)
	stdin.Write([]byte("exit\r"))
	done := make(chan struct{})
	go func() {
		sess.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Timed out waiting for session to exit")
	}
}
