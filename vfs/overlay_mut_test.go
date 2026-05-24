package vfs_test

import (
	"errors"
	"io/fs"
	"os"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/vfs"
)

// attrMem extends memFS with the AttrSetter + LocalRooter + Unwrapper
// interfaces so we can drive the overlay's mount-delegating mutation paths.
type attrMem struct {
	*memFS
	chmodCalls    map[string]os.FileMode
	chtimesCalls  map[string][2]time.Time
	truncateCalls map[string]int64
	linkCalls     [][2]string
	localRoot     string
}

func newAttrMem() *attrMem {
	return &attrMem{
		memFS:         newMemFS(),
		chmodCalls:    map[string]os.FileMode{},
		chtimesCalls:  map[string][2]time.Time{},
		truncateCalls: map[string]int64{},
	}
}

func (m *attrMem) Chmod(p string, mode os.FileMode) error {
	m.chmodCalls[p] = mode
	return nil
}

func (m *attrMem) Chtimes(p string, a, mt time.Time) error {
	m.chtimesCalls[p] = [2]time.Time{a, mt}
	return nil
}

func (m *attrMem) Truncate(p string, size int64) error {
	m.truncateCalls[p] = size
	return nil
}

func (m *attrMem) Link(oldname, newname string) error {
	m.linkCalls = append(m.linkCalls, [2]string{oldname, newname})
	return nil
}

func (m *attrMem) LocalRoot() string { return m.localRoot }

func (m *attrMem) Unwrap(p string) (vfs.FS, string, error) {
	return m, p, nil
}

// --- Chmod / Chtimes / Truncate on plain base ---

func TestOverlay_Chmod_Base(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	if err := o.Chmod("/file.txt", 0o644); err != nil {
		t.Fatal(err)
	}
	if base.chmodCalls["/file.txt"] != 0o644 {
		t.Fatalf("chmod not delegated: %v", base.chmodCalls)
	}
}

func TestOverlay_Chmod_VirtualIsNoop(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	o.Bind("/.ssh/authorized_keys", &stubHandler{content: []byte("k")})
	if err := o.Chmod("/.ssh/authorized_keys", 0o600); err != nil {
		t.Fatal(err)
	}
	// Base should NOT see the call.
	if _, ok := base.chmodCalls["/.ssh/authorized_keys"]; ok {
		t.Fatalf("virtual chmod leaked to base")
	}
}

func TestOverlay_Chmod_NilBaseIsNoop(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	if err := o.Chmod("/anything", 0o600); err != nil {
		t.Fatalf("nil-base chmod errored: %v", err)
	}
}

func TestOverlay_Chmod_NonAttrBaseErrors(t *testing.T) {
	// memFS doesn't implement AttrSetter — base path returns an error.
	o := vfs.NewOverlayFS(newMemFS())
	if err := o.Chmod("/x", 0o600); err == nil {
		t.Fatal("expected error from non-AttrSetter base")
	}
}

func TestOverlay_Chtimes_Base(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	a := time.Unix(100, 0)
	mt := time.Unix(200, 0)
	if err := o.Chtimes("/f.txt", a, mt); err != nil {
		t.Fatal(err)
	}
	got := base.chtimesCalls["/f.txt"]
	if got[0] != a || got[1] != mt {
		t.Fatalf("chtimes mismatch: %v", got)
	}
}

func TestOverlay_Truncate_Base(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	if err := o.Truncate("/f.txt", 42); err != nil {
		t.Fatal(err)
	}
	if base.truncateCalls["/f.txt"] != 42 {
		t.Fatalf("truncate not delegated")
	}
}

func TestOverlay_Truncate_DirIsNoop(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	o.Mkdir("/d") // implicit virtual dir via Bind not applicable; use memFS
	// Direct implicit dir via Bind to register the parent.
	o.Bind("/d/file", &stubHandler{content: []byte("x")})
	// Truncating the virtual dir is a no-op.
	if err := o.Truncate("/d", 0); err != nil {
		t.Fatal(err)
	}
	if _, ok := base.truncateCalls["/d"]; ok {
		t.Fatalf("virtual dir truncate leaked to base")
	}
}

// --- Link ---

func TestOverlay_Link_BaseSupport(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	if err := o.Link("/a", "/b"); err != nil {
		t.Fatal(err)
	}
	if len(base.linkCalls) != 1 {
		t.Fatalf("link not delegated: %v", base.linkCalls)
	}
}

func TestOverlay_Link_VirtualRejected(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	o.Bind("/v.txt", &stubHandler{content: []byte("x")})
	if err := o.Link("/v.txt", "/other"); err == nil {
		t.Fatal("expected error linking virtual file")
	}
	if err := o.Link("/other", "/v.txt"); err == nil {
		t.Fatal("expected error linking to virtual file")
	}
}

func TestOverlay_Link_NilBase(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	if err := o.Link("/a", "/b"); err == nil {
		t.Fatal("expected error with nil base")
	}
}

// --- Rename mutation paths not yet covered by overlay_test ---

func TestOverlay_Rename_BasePath(t *testing.T) {
	base := newMemFS()
	base.files["/old.txt"] = []byte("data")
	o := vfs.NewOverlayFS(base)
	if err := o.Rename("/old.txt", "/new.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := base.files["/new.txt"]; !ok {
		t.Fatalf("expected new path")
	}
}

func TestOverlay_Rename_VirtualSrcRejected(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/virt", &stubHandler{content: []byte("x")})
	if err := o.Rename("/virt", "/other"); err == nil {
		t.Fatal("expected error")
	}
}

func TestOverlay_Rename_VirtualDstRejected(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/virt", &stubHandler{content: []byte("x")})
	if err := o.Rename("/other", "/virt"); err == nil {
		t.Fatal("expected error")
	}
}

func TestOverlay_Rename_NilBaseNotFound(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	err := o.Rename("/a", "/b")
	if !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- LstatInfo (separate code path from StatInfo) ---

func TestOverlay_LstatInfo_VirtualFile(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/k", &stubHandler{content: []byte("x")})
	fi, err := o.LstatInfo("/k")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Name() != "authorized_keys" { // stubFileInfo's name
		t.Fatalf("unexpected name: %s", fi.Name())
	}
}

func TestOverlay_LstatInfo_ImplicitDir(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/d/inside", &stubHandler{content: []byte("x")})
	fi, err := o.LstatInfo("/d")
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatalf("expected dir, got %v", fi)
	}
}

func TestOverlay_LstatInfo_NilBaseRoot(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	fi, err := o.LstatInfo("/")
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatal("/ should be a dir on nil-base overlay")
	}
}

func TestOverlay_LstatInfo_NilBase_NotFound(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	_, err := o.LstatInfo("/nothing")
	if !errors.Is(err, vfs.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// --- LocalRoot ---

func TestOverlay_LocalRoot_FromBase(t *testing.T) {
	base := newAttrMem()
	base.localRoot = "/tmp/cog-data"
	o := vfs.NewOverlayFS(base)
	if got := o.LocalRoot(); got != "/tmp/cog-data" {
		t.Fatalf("LocalRoot = %q, want %q", got, "/tmp/cog-data")
	}
}

func TestOverlay_LocalRoot_NilBase(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	if got := o.LocalRoot(); got != "" {
		t.Fatalf("LocalRoot = %q, want empty", got)
	}
}

func TestOverlay_LocalRoot_BaseLacksInterface(t *testing.T) {
	// memFS doesn't implement LocalRooter.
	o := vfs.NewOverlayFS(newMemFS())
	if got := o.LocalRoot(); got != "" {
		t.Fatalf("LocalRoot = %q, want empty", got)
	}
}

// --- Unwrap ---

func TestOverlay_Unwrap_BaseDelegates(t *testing.T) {
	base := newAttrMem()
	o := vfs.NewOverlayFS(base)
	fsys, sub, err := o.Unwrap("/x")
	if err != nil {
		t.Fatal(err)
	}
	if fsys != vfs.FS(base) {
		t.Fatalf("unexpected fs")
	}
	if sub != "/x" {
		t.Fatalf("unexpected sub: %q", sub)
	}
}

func TestOverlay_Unwrap_VirtualRejected(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/v", &stubHandler{content: []byte("x")})
	_, _, err := o.Unwrap("/v")
	if err == nil {
		t.Fatal("expected error unwrapping virtual file")
	}
}

func TestOverlay_Unwrap_NilBase(t *testing.T) {
	o := vfs.NewOverlayFS(nil)
	_, _, err := o.Unwrap("/x")
	if err == nil {
		t.Fatal("expected error with nil base")
	}
}

func TestOverlay_Unwrap_NonUnwrapperBase(t *testing.T) {
	// memFS doesn't implement Unwrapper.
	base := newMemFS()
	o := vfs.NewOverlayFS(base)
	fsys, sub, err := o.Unwrap("/p")
	if err != nil {
		t.Fatal(err)
	}
	if fsys != vfs.FS(base) || sub != "/p" {
		t.Fatalf("unexpected fs/sub: %v %q", fsys, sub)
	}
}

// --- Cross-mount rename rejected ---

func TestOverlay_Rename_CrossMountRejected(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	mountFS := newMemFS()
	mountFS.files["/inside"] = []byte("data")
	o.Mount("/mnt", mountFS)
	// Destination not in any mount → error.
	if err := o.Rename("/mnt/inside", "/outside"); err == nil {
		t.Fatal("expected cross-mount rename error")
	}
}

func TestOverlay_Link_CrossMountRejected(t *testing.T) {
	o := vfs.NewOverlayFS(newAttrMem())
	mountFS := newAttrMem()
	o.Mount("/mnt", mountFS)
	if err := o.Link("/mnt/a", "/outside"); err == nil {
		t.Fatal("expected cross-mount link error")
	}
}

// --- AttrSetter on mounted FS ---

func TestOverlay_Chmod_DelegatesToMount(t *testing.T) {
	root := newAttrMem()
	mfs := newAttrMem()
	o := vfs.NewOverlayFS(root)
	o.Mount("/m", mfs)
	if err := o.Chmod("/m/file", 0o600); err != nil {
		t.Fatal(err)
	}
	if mfs.chmodCalls["/file"] != 0o600 {
		t.Fatalf("mount chmod missed: %v", mfs.chmodCalls)
	}
	if _, ok := root.chmodCalls["/m/file"]; ok {
		t.Fatalf("root saw mount-path chmod")
	}
}

// --- Sanity: stubFileInfo from existing test data still works through Lstat ---

func TestOverlay_LstatInfo_StubModeIsRegular(t *testing.T) {
	o := vfs.NewOverlayFS(newMemFS())
	o.Bind("/k", &stubHandler{content: []byte("x")})
	fi, err := o.LstatInfo("/k")
	if err != nil {
		t.Fatal(err)
	}
	if fi.IsDir() {
		t.Fatal("virtual file reported as dir")
	}
	// Mode is whatever the stub returns; assert it's a file mode (not 0).
	if fi.Mode()&fs.ModeType != 0 {
		t.Fatalf("unexpected mode type bits: %v", fi.Mode())
	}
}
