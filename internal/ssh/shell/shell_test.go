package shell

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/plugin"
)

func newTestShell(t *testing.T) (*Shell, string) {
	t.Helper()
	dir := t.TempDir()
	// Create some test files and directories.
	os.MkdirAll(filepath.Join(dir, "Documents"), 0o755)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Documents", "notes.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	sh := New("testuser", dir, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	return sh, dir
}

func runCmd(t *testing.T, sh *Shell, command string) (stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	err := sh.Run(context.Background(), command, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		// Non-zero exit is not a test failure — capture stderr.
		_ = err
	}
	return out.String(), errBuf.String()
}

func TestResolveContainment(t *testing.T) {
	sh, dir := newTestShell(t)

	// Absolute virtual path should resolve within filesDir.
	got, err := sh.resolve(dir, "/hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "hello.txt") {
		t.Fatalf("expected %s, got %s", filepath.Join(dir, "hello.txt"), got)
	}

	// Path traversal must be rejected.
	_, err = sh.resolve(dir, "../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal")
	}
}

func TestVirtualPath(t *testing.T) {
	sh, dir := newTestShell(t)

	if vp := sh.virtualPath(dir); vp != "/" {
		t.Fatalf("expected /, got %s", vp)
	}
	if vp := sh.virtualPath(filepath.Join(dir, "Documents")); vp != "/Documents" {
		t.Fatalf("expected /Documents, got %s", vp)
	}
}

func TestPwd(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "pwd")
	if strings.TrimSpace(out) != "/" {
		t.Fatalf("expected /, got %q", strings.TrimSpace(out))
	}
}

func TestEcho(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "echo hello world")
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(out))
	}
}

func TestWhoami(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "whoami")
	if strings.TrimSpace(out) != "testuser" {
		t.Fatalf("expected 'testuser', got %q", strings.TrimSpace(out))
	}
}

func TestCat(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "cat /hello.txt")
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(out))
	}
}

func TestLs(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "ls /")
	if !strings.Contains(out, "Documents") || !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected Documents and hello.txt in output, got %q", out)
	}
}

func TestMkdirAndTouch(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "mkdir /newdir")
	runCmd(t, sh, "touch /newdir/file.txt")

	if _, err := os.Stat(filepath.Join(dir, "newdir", "file.txt")); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

func TestRm(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "touch /deleteme.txt")
	runCmd(t, sh, "rm /deleteme.txt")

	if _, err := os.Stat(filepath.Join(dir, "deleteme.txt")); !os.IsNotExist(err) {
		t.Fatal("expected file to be deleted")
	}
}

func TestPipe(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "echo foo | cat")
	if strings.TrimSpace(out) != "foo" {
		t.Fatalf("expected 'foo', got %q", strings.TrimSpace(out))
	}
}

func TestCommandNotFound(t *testing.T) {
	sh, _ := newTestShell(t)
	_, stderr := runCmd(t, sh, "nonexistent")
	if !strings.Contains(stderr, "command not found") {
		t.Fatalf("expected 'command not found', got %q", stderr)
	}
}

func TestHeadTail(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "head -n 1 /Documents/notes.txt")
	if strings.TrimSpace(out) != "line1" {
		t.Fatalf("expected 'line1', got %q", strings.TrimSpace(out))
	}

	out, _ = runCmd(t, sh, "tail -n 1 /Documents/notes.txt")
	if strings.TrimSpace(out) != "line3" {
		t.Fatalf("expected 'line3', got %q", strings.TrimSpace(out))
	}
}

func TestPluginCommand(t *testing.T) {
	ran := false
	cmds := map[string][]plugin.CommandDef{
		"myplugin": {
			{
				Name:  "greet",
				Short: "say hello",
				Run: func(ctx context.Context, args []string) error {
					ran = true
					return nil
				},
			},
		},
	}

	dir := t.TempDir()
	sh := New("testuser", dir, cmds, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	runCmdWith(t, sh, "myplugin greet")

	if !ran {
		t.Fatal("expected plugin command to run")
	}
}

func runCmdWith(t *testing.T, sh *Shell, command string) (stdout, stderr string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	sh.Run(context.Background(), command, strings.NewReader(""), &out, &errBuf)
	return out.String(), errBuf.String()
}

func TestRedirect(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "echo hello > /output.txt")

	data, err := os.ReadFile(filepath.Join(dir, "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "hello" {
		t.Fatalf("expected 'hello', got %q", strings.TrimSpace(string(data)))
	}
}

func TestCp(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "cp /hello.txt /hello_copy.txt")

	data, err := os.ReadFile(filepath.Join(dir, "hello_copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(string(data)))
	}
}

func TestMv(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "touch /moveme.txt")
	runCmd(t, sh, "mv /moveme.txt /moved.txt")

	if _, err := os.Stat(filepath.Join(dir, "moveme.txt")); !os.IsNotExist(err) {
		t.Fatal("expected source to be gone")
	}
	if _, err := os.Stat(filepath.Join(dir, "moved.txt")); err != nil {
		t.Fatal("expected destination to exist")
	}
}

func TestVariableExpansion(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "echo $USER")
	if strings.TrimSpace(out) != "testuser" {
		t.Fatalf("expected 'testuser', got %q", strings.TrimSpace(out))
	}
}
