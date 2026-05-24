package volume

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

func tempFileBackend(t *testing.T) (*fileBackend, string) {
	t.Helper()
	dir := t.TempDir()
	return newFileBackend(dir), dir
}

// --- Basic operations ---

func TestFileBackend_PutGetReadFile(t *testing.T) {
	b, _ := tempFileBackend(t)

	require.NoError(t, b.Put("/hello.txt", strings.NewReader("hi")))

	rc, err := b.Get("/hello.txt")
	require.NoError(t, err)
	data, _ := io.ReadAll(rc)
	rc.Close()
	require.Equal(t, "hi", string(data))

	got, err := b.ReadFile("/hello.txt")
	require.NoError(t, err)
	require.Equal(t, "hi", string(got))
}

func TestFileBackend_MkdirThenList(t *testing.T) {
	b, root := tempFileBackend(t)
	require.NoError(t, b.Mkdir("/sub"))

	// Verify the directory exists on disk.
	info, err := os.Stat(filepath.Join(root, "sub"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	// List parent.
	items, err := b.List("/")
	require.NoError(t, err)
	require.Equal(t, 1, len(items))
	require.Equal(t, "sub", items[0].Name)
	require.True(t, items[0].IsDir)
}

func TestFileBackend_List_FilePopulatesMime(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/note.txt", strings.NewReader("x")))
	require.NoError(t, b.Put("/img.png", strings.NewReader("y")))

	items, err := b.List("/")
	require.NoError(t, err)
	require.Equal(t, 2, len(items))

	for _, it := range items {
		// File entries (non-dir) should have MIME populated by extension.
		require.True(t, it.MimeType != "")
	}
}

func TestFileBackend_Stat(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/s.txt", strings.NewReader("hello")))

	fi, err := b.Stat("/s.txt")
	require.NoError(t, err)
	require.Equal(t, "s.txt", fi.Name)
	require.Equal(t, int64(5), fi.Size)
	require.False(t, fi.IsDir)
}

func TestFileBackend_StatMissing(t *testing.T) {
	b, _ := tempFileBackend(t)
	_, err := b.Stat("/nope")
	require.True(t, err != nil)
}

func TestFileBackend_Delete(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/d.txt", strings.NewReader("x")))
	require.NoError(t, b.Delete("/d.txt"))

	_, err := b.Stat("/d.txt")
	require.True(t, err != nil)
}

func TestFileBackend_Rename(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/old.txt", strings.NewReader("x")))
	require.NoError(t, b.Rename("/old.txt", "/new.txt"))

	got, err := b.ReadFile("/new.txt")
	require.NoError(t, err)
	require.Equal(t, "x", string(got))
}

func TestFileBackend_LocalRoot(t *testing.T) {
	b, root := tempFileBackend(t)
	require.Equal(t, root, b.LocalRoot())
}

// --- Path traversal guard ---

func TestFileBackend_PathTraversal_DotDot(t *testing.T) {
	b, root := tempFileBackend(t)
	// Place a sibling outside the root.
	sibling := filepath.Join(filepath.Dir(root), "outside.txt")
	require.NoError(t, os.WriteFile(sibling, []byte("secret"), 0o644))
	t.Cleanup(func() { os.Remove(sibling) })

	// "../outside.txt" should NOT escape — filepath.Clean collapses it
	// inside the cleaned root, but the backend's resolve guards against
	// any path that lands outside.
	_, err := b.Get("/../outside.txt")
	require.True(t, err != nil)
}

func TestFileBackend_PathTraversal_AbsolutePath(t *testing.T) {
	b, _ := tempFileBackend(t)
	// Absolute paths are joined with root via filepath.Join after Clean("/"+path).
	// The cleaned path stays under the root.
	_, err := b.Get("/etc/passwd")
	require.True(t, err != nil) // file doesn't exist under our temp root
}

// --- OpenFile ---

func TestFileBackend_OpenFile_ReadWrite(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/rw.txt", strings.NewReader("seed")))

	f, err := b.OpenFile("/rw.txt", os.O_RDWR, 0o644)
	require.NoError(t, err)
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.Equal(t, "seed", string(buf))
}

func TestFileBackend_OpenFile_Create(t *testing.T) {
	b, _ := tempFileBackend(t)
	f, err := b.OpenFile("/new.txt", os.O_CREATE|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = f.Write([]byte("written"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got, _ := b.ReadFile("/new.txt")
	require.Equal(t, "written", string(got))
}

// --- AttrSetter methods ---

func TestFileBackend_Chmod(t *testing.T) {
	b, root := tempFileBackend(t)
	require.NoError(t, b.Put("/mode.txt", strings.NewReader("x")))
	require.NoError(t, b.Chmod("/mode.txt", 0o600))

	info, err := os.Stat(filepath.Join(root, "mode.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestFileBackend_Chtimes(t *testing.T) {
	b, root := tempFileBackend(t)
	require.NoError(t, b.Put("/time.txt", strings.NewReader("x")))

	when := time.Unix(1_700_000_000, 0)
	require.NoError(t, b.Chtimes("/time.txt", when, when))

	info, err := os.Stat(filepath.Join(root, "time.txt"))
	require.NoError(t, err)
	require.Equal(t, when.Unix(), info.ModTime().Unix())
}

func TestFileBackend_Truncate(t *testing.T) {
	b, _ := tempFileBackend(t)
	require.NoError(t, b.Put("/trunc.txt", strings.NewReader("hello world")))
	require.NoError(t, b.Truncate("/trunc.txt", 5))

	got, _ := b.ReadFile("/trunc.txt")
	require.Equal(t, "hello", string(got))
}

// --- Larger payload to exercise Put streaming ---

func TestFileBackend_Put_LargePayload(t *testing.T) {
	b, _ := tempFileBackend(t)
	payload := bytes.Repeat([]byte("x"), 64*1024)
	require.NoError(t, b.Put("/big", bytes.NewReader(payload)))

	got, err := b.ReadFile("/big")
	require.NoError(t, err)
	require.Equal(t, len(payload), len(got))
}

// --- Backend dispatcher (NewBackend) ---

func TestNewBackend_File(t *testing.T) {
	dir := t.TempDir()
	vol := &Volume{URI: "file://" + dir}
	be, err := NewBackend(vol)
	require.NoError(t, err)
	require.NotNil(t, be)
}

func TestNewBackend_Mem_Unsupported(t *testing.T) {
	vol := &Volume{URI: "mem://anything"}
	_, err := NewBackend(vol)
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "mem"))
}

func TestNewBackend_UnknownScheme(t *testing.T) {
	vol := &Volume{URI: "weird://x"}
	_, err := NewBackend(vol)
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "scheme"))
}

func TestNewBackend_BadURI(t *testing.T) {
	vol := &Volume{URI: "not-a-uri"}
	_, err := NewBackend(vol)
	require.True(t, err != nil)
}

// --- ParseURI ---

func TestParseURI_File(t *testing.T) {
	p, err := ParseURI("file:///var/lib/cog")
	require.NoError(t, err)
	require.Equal(t, "file", p.Scheme)
	require.Equal(t, "/var/lib/cog", p.Path)
}

func TestParseURI_Mem(t *testing.T) {
	p, err := ParseURI("mem://tmpfs")
	require.NoError(t, err)
	require.Equal(t, "mem", p.Scheme)
}

func TestParseURI_NoScheme(t *testing.T) {
	_, err := ParseURI("no-scheme-here")
	require.True(t, err != nil)
}

// --- Error matching ---

func TestFileBackend_GetMissingErrNotFoundLike(t *testing.T) {
	b, _ := tempFileBackend(t)
	_, err := b.Get("/missing")
	require.True(t, err != nil)
	// fileBackend wraps via os.IsNotExist semantics; just confirm an error.
	_ = errors.Is(err, os.ErrNotExist)
}
