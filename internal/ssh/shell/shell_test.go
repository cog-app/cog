package shell

import (
	"bytes"
	"context"
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
)

func newTestShell(t *testing.T) (*Shell, string) {
	t.Helper()
	dir := t.TempDir()
	// Create some test files and directories.
	os.MkdirAll(filepath.Join(dir, "Documents"), 0o755)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Documents", "notes.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	sh := New("testuser", dir, nil, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
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
	sh := New("testuser", dir, nil, cmds, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
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
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "Documents"), 0o755)
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Documents", "notes.txt"), []byte("line1\nline2\nline3\n"), 0o644)

	base := &testFileFS{root: dir}
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubKeyHandler{content: "ssh-ed25519 AAAA testkey\n"})

	return New("testuser", dir, ofs, nil, nil, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
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
	// Regular file operations should still work through the overlay.
	out, _ := runCmd(t, sh, "cat /hello.txt")
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", strings.TrimSpace(out))
	}

	// ls should include real files.
	out, _ = runCmd(t, sh, "ls /")
	if !strings.Contains(out, "hello.txt") {
		t.Fatalf("expected hello.txt in ls output, got %q", out)
	}
	if !strings.Contains(out, "Documents") {
		t.Fatalf("expected Documents in ls output, got %q", out)
	}
}

func TestVFSHeadTail(t *testing.T) {
	sh := newVFSTestShell(t)
	out, _ := runCmd(t, sh, "head -n 1 /Documents/notes.txt")
	if strings.TrimSpace(out) != "line1" {
		t.Fatalf("expected 'line1', got %q", strings.TrimSpace(out))
	}
	out, _ = runCmd(t, sh, "tail -n 1 /Documents/notes.txt")
	if strings.TrimSpace(out) != "line3" {
		t.Fatalf("expected 'line3', got %q", strings.TrimSpace(out))
	}
}
