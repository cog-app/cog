package ssh

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/clock"
	"git.duckfam.us/jj-software/cog/internal/config"
	"git.duckfam.us/jj-software/cog/internal/db"
	"git.duckfam.us/jj-software/cog/internal/services"
	"git.duckfam.us/jj-software/cog/internal/settings"
	"git.duckfam.us/jj-software/cog/internal/ssh/keys"
	"git.duckfam.us/jj-software/cog/internal/testing/require"

	gossh "golang.org/x/crypto/ssh"
)

// startTestServer starts an SSH server with one user, testuser, and returns
// a client logged in as them.
func startTestServer(t *testing.T) *gossh.Client {
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
	require.NoError(t, p.Plugins.Start(context.Background()))
	_, err = p.Users.Create("testuser", "testpass")
	require.NoError(t, err)
	require.NoError(t, p.Plugins.EnsureUserDirs("testuser"))

	ks, err := keys.NewStore(p.DB, p.Plugins, p.Users, clock.System())
	require.NoError(t, err)
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
	t.Cleanup(func() { client.Close() })
	return client
}

func TestSSHInteractiveShell(t *testing.T) {
	client := startTestServer(t)

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

// shellSession runs an interactive pty session, typing each line once the
// prompt is up, and returns how the session ended.
func shellSession(t *testing.T, client *gossh.Client, lines ...string) error {
	t.Helper()
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	require.NoError(t, sess.RequestPty("xterm", 24, 80, gossh.TerminalModes{gossh.ECHO: 1}))
	stdin, err := sess.StdinPipe()
	require.NoError(t, err)
	stdout, err := sess.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, sess.Shell())

	prompts := make(chan struct{}, 16)
	go func() {
		var seen strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			seen.Write(buf[:n])
			for strings.Contains(seen.String(), "$ ") {
				rest := seen.String()[strings.Index(seen.String(), "$ ")+2:]
				seen.Reset()
				seen.WriteString(rest)
				prompts <- struct{}{}
			}
			if err != nil {
				return
			}
		}
	}()
	for _, line := range lines {
		select {
		case <-prompts:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for the prompt before %q", line)
		}
		// readline queries the cursor position once the prompt is out;
		// typing into that exchange loses the keystrokes.
		time.Sleep(100 * time.Millisecond)
		_, err := stdin.Write([]byte(line + "\r"))
		require.NoError(t, err)
	}
	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the session to end")
		return nil
	}
}

// exitStatusOf is the status a session ended with.
func exitStatusOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	exitErr, ok := errors.AsType[*gossh.ExitError](err)
	require.True(t, ok, "session ended without an exit status: %v", err)
	return exitErr.ExitStatus()
}

// exit N ends an interactive session with N, taken modulo 256 as a POSIX
// shell does; it used to end every session with 0.
func TestSSHInteractiveExitStatus(t *testing.T) {
	client := startTestServer(t)
	for _, tc := range []struct {
		lines []string
		want  int
	}{
		{[]string{"exit 3"}, 3},
		{[]string{"exit 300"}, 300 & 0xff},
		{[]string{"exit -1"}, 255},
		{[]string{"exit nope"}, 2},
		{[]string{"false", "exit"}, 1}, // bare exit: the last command's status
		{[]string{"exit 0"}, 0},
	} {
		got := exitStatusOf(t, shellSession(t, client, tc.lines...))
		require.Equal(t, tc.want, got, "after %q", tc.lines)
	}
}

// In a command, exit stops what follows it and sets the session's status.
func TestSSHExecExitStopsTheCommand(t *testing.T) {
	client := startTestServer(t)
	sess, err := client.NewSession()
	require.NoError(t, err)
	defer sess.Close()
	out, err := sess.Output("echo before; exit 300; echo after")
	require.Equal(t, 300&0xff, exitStatusOf(t, err))
	require.Contains(t, string(out), "before")
	require.NotContains(t, string(out), "after")
}
