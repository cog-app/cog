package vfs_test

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

// stubHandler implements vfs.FileHandler for testing.
type stubHandler struct {
	content []byte
}

func (h *stubHandler) Open(flag int, perm os.FileMode) (vfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return &stubFile{buf: bytes.NewBuffer(nil), handler: h}, nil
	}
	return &stubFile{reader: bytes.NewReader(h.content)}, nil
}

func (h *stubHandler) Stat() (fs.FileInfo, error) {
	return &stubFileInfo{name: "authorized_keys", size: int64(len(h.content))}, nil
}

type stubFile struct {
	reader  *bytes.Reader
	buf     *bytes.Buffer
	handler *stubHandler
}

func (f *stubFile) Read(p []byte) (int, error) {
	if f.reader != nil {
		return f.reader.Read(p)
	}
	return 0, io.EOF
}

func (f *stubFile) Write(p []byte) (int, error) {
	if f.buf != nil {
		return f.buf.Write(p)
	}
	return 0, fs.ErrPermission
}

func (f *stubFile) ReadAt(p []byte, off int64) (int, error) {
	if f.reader != nil {
		return f.reader.ReadAt(p, off)
	}
	return 0, io.EOF
}

func (f *stubFile) WriteAt([]byte, int64) (int, error) { return 0, fs.ErrPermission }
func (f *stubFile) Seek(int64, int) (int64, error)     { return 0, nil }
func (f *stubFile) Stat() (fs.FileInfo, error)         { return &stubFileInfo{name: "authorized_keys"}, nil }
func (f *stubFile) Truncate(int64) error               { return nil }

func (f *stubFile) Close() error {
	if f.handler != nil && f.buf != nil {
		f.handler.content = f.buf.Bytes()
	}
	return nil
}

type stubFileInfo struct {
	name string
	size int64
}

func (i *stubFileInfo) Name() string       { return i.name }
func (i *stubFileInfo) Size() int64        { return i.size }
func (i *stubFileInfo) Mode() fs.FileMode  { return 0o600 }
func (i *stubFileInfo) ModTime() time.Time { return time.Now() }
func (i *stubFileInfo) IsDir() bool        { return false }
func (i *stubFileInfo) Sys() any           { return nil }

// memFS is a minimal in-memory FS for testing the base layer.
type memFS struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMemFS() *memFS {
	return &memFS{
		files: make(map[string][]byte),
		dirs:  map[string]bool{"/": true},
	}
}

func (m *memFS) List(path string) ([]vfs.FileInfo, error) {
	if path == "/" || path == "" || path == "." {
		path = "/"
	}
	var items []vfs.FileInfo
	for f, data := range m.files {
		dir := parentDir(f)
		if dir == path {
			items = append(items, vfs.FileInfo{Name: baseName(f), Path: f, Size: int64(len(data))})
		}
	}
	for d := range m.dirs {
		dir := parentDir(d)
		if dir == path && d != "/" {
			items = append(items, vfs.FileInfo{Name: baseName(d), Path: d, IsDir: true})
		}
	}
	return items, nil
}

func (m *memFS) Stat(p string) (*vfs.FileInfo, error) {
	if data, ok := m.files[p]; ok {
		return &vfs.FileInfo{Name: baseName(p), Path: p, Size: int64(len(data))}, nil
	}
	if m.dirs[p] {
		return &vfs.FileInfo{Name: baseName(p), Path: p, IsDir: true}, nil
	}
	return nil, vfs.ErrNotFound
}

func (m *memFS) Get(p string) (io.ReadCloser, error) {
	if data, ok := m.files[p]; ok {
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	return nil, vfs.ErrNotFound
}

func (m *memFS) Put(p string, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	m.files[p] = data
	return nil
}

func (m *memFS) Mkdir(p string) error {
	m.dirs[p] = true
	return nil
}

func (m *memFS) Delete(p string) error {
	delete(m.files, p)
	delete(m.dirs, p)
	return nil
}

func (m *memFS) Rename(old, new string) error {
	if data, ok := m.files[old]; ok {
		m.files[new] = data
		delete(m.files, old)
		return nil
	}
	return vfs.ErrNotFound
}

func (m *memFS) ReadFile(p string) ([]byte, error) {
	if data, ok := m.files[p]; ok {
		return data, nil
	}
	return nil, vfs.ErrNotFound
}

func parentDir(p string) string {
	for i := len(p) - 1; i > 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "/"
}

func baseName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

func TestOverlayFS_BindStat(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("ssh-ed25519 AAAA")})

	fi, err := ofs.Stat("/.ssh/authorized_keys")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Name != "authorized_keys" {
		t.Fatalf("expected name authorized_keys, got %s", fi.Name)
	}
	if fi.IsDir {
		t.Fatal("expected file, not directory")
	}
}

func TestOverlayFS_ImplicitDir(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	fi, err := ofs.Stat("/.ssh")
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir {
		t.Fatal("expected implicit dir to be a directory")
	}
	if fi.Name != ".ssh" {
		t.Fatalf("expected name .ssh, got %s", fi.Name)
	}
}

func TestOverlayFS_RootListing(t *testing.T) {
	base := newMemFS()
	base.Put("/hello.txt", bytes.NewReader([]byte("hi")))
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	items, err := ofs.List("/")
	if err != nil {
		t.Fatal(err)
	}

	names := make(map[string]bool)
	for _, fi := range items {
		names[fi.Name] = true
	}
	if !names["hello.txt"] {
		t.Fatal("expected hello.txt in root listing")
	}
	if !names[".ssh"] {
		t.Fatal("expected .ssh in root listing")
	}
}

func TestOverlayFS_DirListing(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	items, err := ofs.List("/.ssh")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].Name != "authorized_keys" {
		t.Fatalf("expected authorized_keys, got %s", items[0].Name)
	}
}

func TestOverlayFS_Passthrough(t *testing.T) {
	base := newMemFS()
	base.Put("/readme.txt", bytes.NewReader([]byte("hello")))
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	data, err := ofs.ReadFile("/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(data))
	}
}

func TestOverlayFS_MkdirImplicit(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	// Mkdir on implicit dir should be a no-op.
	if err := ofs.Mkdir("/.ssh"); err != nil {
		t.Fatalf("expected no-op mkdir, got error: %v", err)
	}
}

func TestOverlayFS_DeleteVirtual(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	err := ofs.Delete("/.ssh/authorized_keys")
	if err == nil {
		t.Fatal("expected error deleting virtual file")
	}

	err = ofs.Delete("/.ssh")
	if err == nil {
		t.Fatal("expected error deleting virtual directory")
	}
}

func TestOverlayFS_ReadWrite(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	h := &stubHandler{content: []byte("initial")}
	ofs.Bind("/.ssh/authorized_keys", h)

	// Read via Get.
	rc, err := ofs.Get("/.ssh/authorized_keys")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "initial" {
		t.Fatalf("expected 'initial', got %q", string(data))
	}

	// Write via Put.
	err = ofs.Put("/.ssh/authorized_keys", bytes.NewReader([]byte("updated")))
	if err != nil {
		t.Fatal(err)
	}
	if string(h.content) != "updated" {
		t.Fatalf("expected handler content 'updated', got %q", string(h.content))
	}
}

func TestOverlayFS_ReadDirEntries(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	entries, err := ofs.ReadDirEntries("/.ssh")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name() != "authorized_keys" {
		t.Fatalf("expected authorized_keys, got %s", entries[0].Name())
	}
}

func TestOverlayFS_RootDirEntries(t *testing.T) {
	base := newMemFS()
	base.Put("/file.txt", bytes.NewReader([]byte("data")))
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key")})

	entries, err := ofs.ReadDirEntries("/")
	if err != nil {
		t.Fatal(err)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["file.txt"] {
		t.Fatal("expected file.txt in root dir entries")
	}
	if !names[".ssh"] {
		t.Fatal("expected .ssh in root dir entries")
	}
}

func TestOverlayFS_StatInfo(t *testing.T) {
	base := newMemFS()
	ofs := vfs.NewOverlayFS(base)
	ofs.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("key data")})

	fi, err := ofs.StatInfo("/.ssh/authorized_keys")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Name() != "authorized_keys" {
		t.Fatalf("expected authorized_keys, got %s", fi.Name())
	}
	if fi.IsDir() {
		t.Fatal("expected file, not directory")
	}

	// Implicit dir.
	fi, err = ofs.StatInfo("/.ssh")
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatal("expected directory")
	}
}
