package shell

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/vfs"

	"mvdan.cc/sh/v3/interp"
)

func newTestShell(t testing.TB) (*Shell, string) {
	t.Helper()
	return newShell(t, nil)
}

// newShell returns a shell on a VFS like a session's, with the files of a
// fresh directory at /files, and that directory.
func newShell(t testing.TB, cmds map[string][]plugin.CommandDef) (*Shell, string) {
	t.Helper()
	dir := t.TempDir()
	// Create some test files and directories.
	if err := os.MkdirAll(filepath.Join(dir, "Documents"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Documents", "notes.txt"), []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ofs := vfs.NewOverlayFS(nil) // nil base — pure virtual root
	ofs.Mount("/files", &testFileFS{root: dir})
	ofs.Bind("/.ssh/authorized_keys", &stubKeyHandler{content: "ssh-ed25519 AAAA testkey\n"})
	ofs.Bind("/dev/null", &devNullHandler{})
	ofs.Bind("/dev/zero", &devZeroHandler{})
	ofs.Bind("/dev/urandom", &devURandHandler{})

	return New("testuser", ofs, cmds, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))), dir
}

func writeFiles(t testing.TB, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// runStatus runs command and returns its output and exit status.
func runStatus(t testing.TB, sh *Shell, command string) (stdout, stderr string, status int) {
	t.Helper()
	var out, errBuf strings.Builder
	err := sh.Run(context.Background(), command, strings.NewReader(""), &out, &errBuf)
	if err != nil {
		es, ok := errors.AsType[interp.ExitStatus](err)
		if !ok {
			t.Fatalf("%s: %v", command, err)
		}
		status = int(es)
	}
	return out.String(), errBuf.String(), status
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

func TestPwd(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "pwd")
	if strings.TrimSpace(out) != "/files" {
		t.Fatalf("expected /files, got %q", strings.TrimSpace(out))
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
	out, _ := runCmd(t, sh, "cat /files/hello.txt")
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(out))
	}
}

func TestLs(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "ls /files")
	if !strings.Contains(out, "Documents") || !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected Documents and hello.txt in output, got %q", out)
	}
}

func TestMkdirAndTouch(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "mkdir /files/newdir")
	runCmd(t, sh, "touch /files/newdir/file.txt")

	if _, err := os.Stat(filepath.Join(dir, "newdir", "file.txt")); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}

func TestRm(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "touch /files/deleteme.txt")
	runCmd(t, sh, "rm /files/deleteme.txt")

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
	out, _ := runCmd(t, sh, "head -n 1 /files/Documents/notes.txt")
	if strings.TrimSpace(out) != "line1" {
		t.Fatalf("expected 'line1', got %q", strings.TrimSpace(out))
	}

	out, _ = runCmd(t, sh, "tail -n 1 /files/Documents/notes.txt")
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

	sh, _ := newShell(t, cmds)
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
	runCmd(t, sh, "echo hello > /files/output.txt")

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
	runCmd(t, sh, "cp /files/hello.txt /files/hello_copy.txt")

	data, err := os.ReadFile(filepath.Join(dir, "hello_copy.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(string(data)))
	}
}

func TestCpRecursive(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "mkdir /files/Documents/sub && echo deep > /files/Documents/sub/deep.txt")

	if _, stderr := runCmd(t, sh, "cp /files/Documents /files/Copy"); !strings.Contains(stderr, "omitting directory") {
		t.Fatalf("expected cp without -r to refuse a directory, got %q", stderr)
	}
	if _, stderr := runCmd(t, sh, "cp -r /files/Documents /files/Copy"); stderr != "" {
		t.Fatal(stderr)
	}
	for _, f := range []string{"Copy/notes.txt", "Copy/sub/deep.txt", "Documents/notes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
	}

	if _, stderr := runCmd(t, sh, "cp -R /files/Documents /files/Copy"); stderr != "" {
		t.Fatal(stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "Copy/Documents/sub/deep.txt")); err != nil {
		t.Fatal("expected a copy into an existing directory to land inside it:", err)
	}

	if _, stderr := runCmd(t, sh, "cp -r /files/Documents /files/Documents/sub"); !strings.Contains(stderr, "into itself") {
		t.Fatalf("expected copying a directory into itself to fail, got %q", stderr)
	}
}

func TestCpMultiple(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "mkdir /files/Dest && touch /files/a.txt /files/b.txt")

	if _, stderr := runCmd(t, sh, "cp /files/a.txt /files/b.txt /files/c.txt"); !strings.Contains(stderr, "not a directory") {
		t.Fatalf("expected several sources to need a directory, got %q", stderr)
	}
	if _, stderr := runCmd(t, sh, "cd /files && cp *.txt Dest/"); stderr != "" {
		t.Fatal(stderr)
	}
	for _, f := range []string{"a.txt", "b.txt", "hello.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "Dest", f)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMvMultiple(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "mkdir /files/Dest && touch /files/a.txt /files/b.txt")

	if _, stderr := runCmd(t, sh, "mv /files/a.txt /files/nosuch.txt /files/b.txt /files/Dest"); stderr == "" {
		t.Fatal("expected the missing source to be reported")
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "Dest", f)); err != nil {
			t.Fatal("expected the other sources to move:", err)
		}
	}
}

func TestMv(t *testing.T) {
	sh, dir := newTestShell(t)
	runCmd(t, sh, "touch /files/moveme.txt")
	runCmd(t, sh, "mv /files/moveme.txt /files/moved.txt")

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

// --- VFS overlay tests ---

// testFileFS wraps a local directory as a vfs.FS + FileOpener + DirReader for testing.
type testFileFS struct {
	root string
}

func (f *testFileFS) resolve(p string) string {
	return filepath.Join(f.root, filepath.FromSlash(p))
}

func (f *testFileFS) List(p string) ([]vfs.FileInfo, error) {
	entries, err := os.ReadDir(f.resolve(p))
	if err != nil {
		return nil, err
	}
	var items []vfs.FileInfo
	for _, e := range entries {
		info, _ := e.Info()
		items = append(items, vfs.FileInfo{
			Name:    e.Name(),
			Path:    filepath.Join(p, e.Name()),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   e.IsDir(),
		})
	}
	return items, nil
}

func (f *testFileFS) Stat(p string) (*vfs.FileInfo, error) {
	info, err := os.Stat(f.resolve(p))
	if err != nil {
		return nil, err
	}
	return &vfs.FileInfo{
		Name:    info.Name(),
		Path:    p,
		Size:    info.Size(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (f *testFileFS) Get(p string) (io.ReadCloser, error) {
	return os.Open(f.resolve(p))
}

func (f *testFileFS) Put(p string, content io.Reader) error {
	out, err := os.Create(f.resolve(p))
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, content)
	return err
}

func (f *testFileFS) Mkdir(p string) error {
	return os.MkdirAll(f.resolve(p), 0o755)
}

func (f *testFileFS) Delete(p string) error {
	return os.RemoveAll(f.resolve(p))
}

func (f *testFileFS) Rename(oldPath, newPath string) error {
	return os.Rename(f.resolve(oldPath), f.resolve(newPath))
}

func (f *testFileFS) ReadFile(p string) ([]byte, error) {
	return os.ReadFile(f.resolve(p))
}

func (f *testFileFS) OpenFile(p string, flag int, perm os.FileMode) (vfs.File, error) {
	return os.OpenFile(f.resolve(p), flag, perm)
}

func (f *testFileFS) StatInfo(p string) (fs.FileInfo, error) {
	return os.Stat(f.resolve(p))
}

func (f *testFileFS) LstatInfo(p string) (fs.FileInfo, error) {
	return os.Lstat(f.resolve(p))
}

func (f *testFileFS) ReadDirEntries(p string) ([]fs.DirEntry, error) {
	return os.ReadDir(f.resolve(p))
}

// stubKeyHandler is a minimal vfs.FileHandler for testing virtual files.
type stubKeyHandler struct {
	content string
}

func (h *stubKeyHandler) Stat() (fs.FileInfo, error) {
	return &stubKeyInfo{size: int64(len(h.content))}, nil
}

func (h *stubKeyHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND) != 0 {
		return &stubKeyFile{handler: h, buf: &bytes.Buffer{}}, nil
	}
	return &stubKeyFile{reader: bytes.NewReader([]byte(h.content))}, nil
}

type stubKeyInfo struct {
	size int64
}

func (i *stubKeyInfo) Name() string       { return "authorized_keys" }
func (i *stubKeyInfo) Size() int64        { return i.size }
func (i *stubKeyInfo) Mode() fs.FileMode  { return 0o600 }
func (i *stubKeyInfo) ModTime() time.Time { return time.Now() }
func (i *stubKeyInfo) IsDir() bool        { return false }
func (i *stubKeyInfo) Sys() any           { return nil }

type stubKeyFile struct {
	reader  *bytes.Reader
	buf     *bytes.Buffer
	handler *stubKeyHandler
}

func (f *stubKeyFile) Read(p []byte) (int, error) {
	if f.reader != nil {
		return f.reader.Read(p)
	}
	return 0, io.EOF
}

func (f *stubKeyFile) ReadAt(p []byte, off int64) (int, error) {
	if f.reader != nil {
		return f.reader.ReadAt(p, off)
	}
	return 0, io.EOF
}

func (f *stubKeyFile) Write(p []byte) (int, error) {
	if f.buf != nil {
		return f.buf.Write(p)
	}
	return 0, fs.ErrPermission
}

func (f *stubKeyFile) WriteAt([]byte, int64) (int, error) { return 0, fs.ErrPermission }
func (f *stubKeyFile) Seek(int64, int) (int64, error)     { return 0, nil }
func (f *stubKeyFile) Stat() (fs.FileInfo, error)         { return &stubKeyInfo{}, nil }
func (f *stubKeyFile) Truncate(int64) error               { return nil }

func (f *stubKeyFile) Close() error {
	if f.handler != nil && f.buf != nil {
		f.handler.content += f.buf.String()
	}
	return nil
}

func newVFSTestShell(t *testing.T) *Shell {
	t.Helper()
	sh, _ := newTestShell(t)
	return sh
}

func TestCatAuthorizedKeys(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "cat /.ssh/authorized_keys")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(out, "ssh-ed25519 AAAA testkey") {
		t.Fatalf("expected authorized_keys content, got %q", out)
	}
}

func TestLsRootShowsSSH(t *testing.T) {
	sh := newVFSTestShell(t)
	out, _ := runCmd(t, sh, "ls -a /")
	if !strings.Contains(out, ".ssh") {
		t.Fatalf("expected .ssh in ls -a / output, got %q", out)
	}
	if !strings.Contains(out, "files") {
		t.Fatalf("expected files in ls -a / output, got %q", out)
	}
}

func TestLsSSHDir(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "ls /.ssh")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(out, "authorized_keys") {
		t.Fatalf("expected authorized_keys in ls /.ssh output, got %q", out)
	}
}

func TestSSHCopyIDSequence(t *testing.T) {
	sh := newVFSTestShell(t)

	// Simulate ssh-copy-id: umask 077, then write to .ssh/authorized_keys via redirect.
	// umask should succeed (no-op).
	_, stderr := runCmd(t, sh, "umask 077")
	if stderr != "" {
		t.Fatalf("umask failed: %s", stderr)
	}

	// cat should read the existing content.
	out, _ := runCmd(t, sh, "cat /.ssh/authorized_keys")
	if !strings.Contains(out, "ssh-ed25519 AAAA testkey") {
		t.Fatalf("expected existing key, got %q", out)
	}

	// Verify stat on .ssh works (test -d .ssh).
	_, stderr = runCmd(t, sh, "test -d /.ssh")
	if stderr != "" {
		t.Fatalf("test -d .ssh failed: %s", stderr)
	}
}

func TestVFSPassthrough(t *testing.T) {
	sh := newVFSTestShell(t)
	// Regular file operations should still work through the mount.
	out, _ := runCmd(t, sh, "cat /files/hello.txt")
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(out))
	}

	// ls /files should include real files.
	out, _ = runCmd(t, sh, "ls /files")
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected hello.txt in ls output, got %q", out)
	}
	if !strings.Contains(out, "Documents") {
		t.Fatalf("expected Documents in ls output, got %q", out)
	}
}

func TestCdToRoot(t *testing.T) {
	sh := newVFSTestShell(t)
	// cd / should navigate to virtual root.
	out, stderr := runCmd(t, sh, "cd / && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/" {
		t.Fatalf("expected /, got %q", strings.TrimSpace(out))
	}
}

func TestCdDotDotFromFiles(t *testing.T) {
	sh := newVFSTestShell(t)
	// cd .. from /files should go to /
	out, stderr := runCmd(t, sh, "cd .. && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/" {
		t.Fatalf("expected /, got %q", strings.TrimSpace(out))
	}
}

func TestCdToFiles(t *testing.T) {
	sh := newVFSTestShell(t)
	// cd /files/Documents should work.
	out, stderr := runCmd(t, sh, "cd /files/Documents && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/files/Documents" {
		t.Fatalf("expected /files/Documents, got %q", strings.TrimSpace(out))
	}
}

func TestCdRelativeFromFiles(t *testing.T) {
	sh := newVFSTestShell(t)
	// cd Documents (relative) from /files should work.
	out, stderr := runCmd(t, sh, "cd Documents && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/files/Documents" {
		t.Fatalf("expected /files/Documents, got %q", strings.TrimSpace(out))
	}
}

func TestCdFilesFromRoot(t *testing.T) {
	sh := newVFSTestShell(t)
	// From /, cd files should go to /files.
	out, stderr := runCmd(t, sh, "cd / && cd files && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/files" {
		t.Fatalf("expected /files, got %q", strings.TrimSpace(out))
	}
}

func TestLsFromRoot(t *testing.T) {
	sh := newVFSTestShell(t)
	// ls from / should show files and .ssh.
	out, _ := runCmd(t, sh, "cd / && ls -a")
	if !strings.Contains(out, "files") {
		t.Fatalf("expected files in ls from root, got %q", out)
	}
	if !strings.Contains(out, ".ssh") {
		t.Fatalf("expected .ssh in ls from root, got %q", out)
	}
}

func TestVFSHeadTail(t *testing.T) {
	sh := newVFSTestShell(t)
	out, _ := runCmd(t, sh, "head -n 1 /files/Documents/notes.txt")
	if strings.TrimSpace(out) != "line1" {
		t.Fatalf("expected 'line1', got %q", strings.TrimSpace(out))
	}
	out, _ = runCmd(t, sh, "tail -n 1 /files/Documents/notes.txt")
	if strings.TrimSpace(out) != "line3" {
		t.Fatalf("expected 'line3', got %q", strings.TrimSpace(out))
	}
}

// --- Device file test stubs ---

type devNullHandler struct{}

func (h *devNullHandler) Stat() (fs.FileInfo, error) { return &devTestInfo{name: "null"}, nil }
func (h *devNullHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	return &devTestFile{kind: 0}, nil
}

type devZeroHandler struct{}

func (h *devZeroHandler) Stat() (fs.FileInfo, error) { return &devTestInfo{name: "zero"}, nil }
func (h *devZeroHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	return &devTestFile{kind: 1}, nil
}

type devURandHandler struct{}

func (h *devURandHandler) Stat() (fs.FileInfo, error) { return &devTestInfo{name: "urandom"}, nil }
func (h *devURandHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	return &devTestFile{kind: 2}, nil
}

type devTestInfo struct{ name string }

func (i *devTestInfo) Name() string       { return i.name }
func (i *devTestInfo) Size() int64        { return 0 }
func (i *devTestInfo) Mode() fs.FileMode  { return 0o666 }
func (i *devTestInfo) ModTime() time.Time { return time.Time{} }
func (i *devTestInfo) IsDir() bool        { return false }
func (i *devTestInfo) Sys() any           { return nil }

type devTestFile struct{ kind int } // 0=null, 1=zero, 2=urandom

func (f *devTestFile) Read(p []byte) (int, error) {
	switch f.kind {
	case 0:
		return 0, io.EOF
	case 1:
		clear(p)
		return len(p), nil
	case 2:
		return rand.Read(p)
	}
	return 0, io.EOF
}

func (f *devTestFile) ReadAt(p []byte, _ int64) (int, error)  { return f.Read(p) }
func (f *devTestFile) Write(p []byte) (int, error)            { return len(p), nil }
func (f *devTestFile) WriteAt(p []byte, _ int64) (int, error) { return len(p), nil }
func (f *devTestFile) Seek(_ int64, _ int) (int64, error)     { return 0, nil }
func (f *devTestFile) Truncate(_ int64) error                 { return nil }
func (f *devTestFile) Close() error                           { return nil }
func (f *devTestFile) Stat() (fs.FileInfo, error)             { return &devTestInfo{}, nil }

// --- Device file tests ---

func TestRedirectToDevNull(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "echo hello > /dev/null")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if out != "" {
		t.Fatalf("expected no output, got %q", out)
	}
}

func TestCatDevNull(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "cat /dev/null")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if out != "" {
		t.Fatalf("expected empty output, got %q", out)
	}
}

func TestCdDevDir(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "cd /dev && pwd")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if strings.TrimSpace(out) != "/dev" {
		t.Fatalf("expected /dev, got %q", strings.TrimSpace(out))
	}
}

func TestTree(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "tree /files")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(out, "Documents") {
		t.Fatalf("expected Documents in tree output, got %q", out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected hello.txt in tree output, got %q", out)
	}
	if !strings.Contains(out, "notes.txt") {
		t.Fatalf("expected notes.txt in tree output, got %q", out)
	}
	// Verify tree connectors are present.
	if !strings.Contains(out, "├── ") && !strings.Contains(out, "└── ") {
		t.Fatalf("expected tree connectors in output, got %q", out)
	}
	// Verify summary line.
	if !strings.Contains(out, "directories") || !strings.Contains(out, "files") {
		t.Fatalf("expected summary line, got %q", out)
	}
}

func TestTreeWorkingDir(t *testing.T) {
	sh, _ := newTestShell(t)
	out, stderr := runCmd(t, sh, "tree")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(out, "Documents") {
		t.Fatalf("expected Documents in tree output, got %q", out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected hello.txt in tree output, got %q", out)
	}
}

func TestLsDevDir(t *testing.T) {
	sh := newVFSTestShell(t)
	out, stderr := runCmd(t, sh, "ls /dev")
	if stderr != "" {
		t.Fatalf("unexpected stderr: %s", stderr)
	}
	if !strings.Contains(out, "null") {
		t.Fatalf("expected null in ls /dev, got %q", out)
	}
	if !strings.Contains(out, "zero") {
		t.Fatalf("expected zero in ls /dev, got %q", out)
	}
	if !strings.Contains(out, "urandom") {
		t.Fatalf("expected urandom in ls /dev, got %q", out)
	}
}

func TestHelpCommand(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "help")
	if !strings.Contains(out, "ls") {
		t.Fatalf("expected ls in help output, got %q", out)
	}
	if !strings.Contains(out, "cd") {
		t.Fatalf("expected cd in help output, got %q", out)
	}
}

func TestHelpSubcommand(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "help ls")
	if !strings.Contains(out, "-a") {
		t.Fatalf("expected -a flag in help ls output, got %q", out)
	}
	if !strings.Contains(out, "-l") {
		t.Fatalf("expected -l flag in help ls output, got %q", out)
	}
}

func TestLsHelp(t *testing.T) {
	sh, _ := newTestShell(t)
	out, _ := runCmd(t, sh, "ls --help")
	if !strings.Contains(out, "-a") {
		t.Fatalf("expected -a flag in ls --help output, got %q", out)
	}
}

func TestFlagParsing(t *testing.T) {
	sh, _ := newTestShell(t)

	// ls -al should work.
	out, _ := runCmd(t, sh, "ls -a -l /files")
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected hello.txt in ls -al output, got %q", out)
	}

	// rm -rf should parse flags.
	runCmd(t, sh, "mkdir /files/tmpdir")
	runCmd(t, sh, "touch /files/tmpdir/file.txt")
	_, stderr := runCmd(t, sh, "rm -r -f /files/tmpdir")
	if stderr != "" {
		t.Fatalf("rm -rf should succeed, stderr: %s", stderr)
	}

	// head -n 1 should parse line count.
	out, _ = runCmd(t, sh, "head -n 1 /files/Documents/notes.txt")
	if strings.TrimSpace(out) != "line1" {
		t.Fatalf("expected 'line1', got %q", strings.TrimSpace(out))
	}
}

func TestRmDirectoryNeedsRecursive(t *testing.T) {
	sh, dir := newTestShell(t)
	_, stderr := runCmd(t, sh, "rm /files/Documents")
	if !strings.Contains(stderr, "is a directory") {
		t.Fatalf("expected a refusal, got %q", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "Documents", "notes.txt")); err != nil {
		t.Fatalf("rm without -r removed the directory: %v", err)
	}
}

func TestMkdirWithoutParents(t *testing.T) {
	sh, dir := newTestShell(t)
	_, stderr := runCmd(t, sh, "mkdir /files/a/b")
	if stderr == "" {
		t.Fatal("expected mkdir without -p to refuse a missing parent")
	}
	_, stderr = runCmd(t, sh, "mkdir -p /files/a/b")
	if stderr != "" {
		t.Fatalf("mkdir -p: %s", stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "a", "b")); err != nil {
		t.Fatal(err)
	}
	if _, stderr = runCmd(t, sh, "mkdir /files/a"); !strings.Contains(stderr, "exists") {
		t.Fatalf("expected mkdir of an existing directory to refuse, got %q", stderr)
	}
}
