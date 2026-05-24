package volume_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/vfs"
)

// fileBackendForTest returns a public file-backed vfs.FS via NewBackend so
// we can drive the MountFS layer.
func fileBackendForTest(t *testing.T) (vfs.FS, string) {
	t.Helper()
	dir := t.TempDir()
	be, err := volume.NewBackend(&volume.Volume{URI: "file://" + dir})
	require.NoError(t, err)
	return be, dir
}

func newMount(t *testing.T) (*volume.MountFS, vfs.FS, string) {
	t.Helper()
	be, root := fileBackendForTest(t)
	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: be},
	})
	return mfs, be, root
}

// --- Basic resolution + ops ---

func TestMountFS_Put_Get_Root(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/hello.txt", strings.NewReader("hi")))

	got, err := mfs.ReadFile("/hello.txt")
	require.NoError(t, err)
	require.Equal(t, "hi", string(got))
}

func TestMountFS_Stat(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/s.txt", strings.NewReader("xy")))

	fi, err := mfs.Stat("/s.txt")
	require.NoError(t, err)
	require.Equal(t, int64(2), fi.Size)
}

func TestMountFS_List_MergesMountPoints(t *testing.T) {
	be1, _ := fileBackendForTest(t)
	be2, _ := fileBackendForTest(t)
	require.NoError(t, be2.Put("/inside.txt", strings.NewReader("x")))

	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: be1},
		{UserPath: "/sub", Backend: be2},
	})

	// Listing root surfaces the /sub mount as a synthetic directory entry.
	items, err := mfs.List("/")
	require.NoError(t, err)
	var sawSub bool
	for _, it := range items {
		if it.Name == "sub" && it.IsDir {
			sawSub = true
		}
	}
	require.True(t, sawSub)

	// Listing /sub goes through the mount.
	items, err = mfs.List("/sub")
	require.NoError(t, err)
	require.Equal(t, 1, len(items))
	require.Equal(t, "inside.txt", items[0].Name)
}

func TestMountFS_Mkdir_Delete(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Mkdir("/sub"))

	// Put a file in the new dir.
	require.NoError(t, mfs.Put("/sub/f.txt", strings.NewReader("x")))
	_, err := mfs.Stat("/sub/f.txt")
	require.NoError(t, err)

	// Delete cascades on the backend.
	require.NoError(t, mfs.Delete("/sub/f.txt"))
	_, err = mfs.Stat("/sub/f.txt")
	require.True(t, err != nil)
}

func TestMountFS_Rename_SameMount(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/old.txt", strings.NewReader("x")))
	require.NoError(t, mfs.Rename("/old.txt", "/new.txt"))

	got, err := mfs.ReadFile("/new.txt")
	require.NoError(t, err)
	require.Equal(t, "x", string(got))
}

func TestMountFS_Rename_CrossMountRejected(t *testing.T) {
	be1, _ := fileBackendForTest(t)
	be2, _ := fileBackendForTest(t)
	require.NoError(t, be1.Put("/file.txt", strings.NewReader("x")))

	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/a", Backend: be1},
		{UserPath: "/b", Backend: be2},
	})

	err := mfs.Rename("/a/file.txt", "/b/file.txt")
	require.True(t, err != nil)
	require.True(t, errors.Is(err, vfs.ErrCrossMount))
}

// --- ReadOnly enforcement ---

func TestMountFS_ReadOnly_BlocksMutations(t *testing.T) {
	be, _ := fileBackendForTest(t)
	require.NoError(t, be.Put("/r.txt", strings.NewReader("x")))

	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: be, ReadOnly: true},
	})

	// GET still works.
	got, err := mfs.ReadFile("/r.txt")
	require.NoError(t, err)
	require.Equal(t, "x", string(got))

	// Writes are refused.
	for _, do := range []struct {
		name string
		fn   func() error
	}{
		{"Put", func() error { return mfs.Put("/x", strings.NewReader("y")) }},
		{"Mkdir", func() error { return mfs.Mkdir("/d") }},
		{"Delete", func() error { return mfs.Delete("/r.txt") }},
		{"Rename", func() error { return mfs.Rename("/r.txt", "/q.txt") }},
		{"Chmod", func() error { return mfs.Chmod("/r.txt", 0o600) }},
		{"Chtimes", func() error { return mfs.Chtimes("/r.txt", time.Now(), time.Now()) }},
		{"Truncate", func() error { return mfs.Truncate("/r.txt", 0) }},
		{"Link", func() error { return mfs.Link("/r.txt", "/lnk") }},
	} {
		err := do.fn()
		require.True(t, err != nil)
		if !errors.Is(err, vfs.ErrReadOnly) {
			t.Fatalf("%s: expected ErrReadOnly, got %v", do.name, err)
		}
	}
}

// --- Unwrap ---

func TestMountFS_Unwrap(t *testing.T) {
	be, _ := fileBackendForTest(t)
	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/cloud", Backend: be},
	})

	got, sub, err := mfs.Unwrap("/cloud/inside")
	require.NoError(t, err)
	require.True(t, got != nil)
	// sub is the subpath inside the mount's backend.
	require.True(t, strings.HasSuffix(sub, "/inside"))
}

func TestMountFS_Unwrap_NoMount(t *testing.T) {
	mfs := volume.NewMountFS(nil)
	_, _, err := mfs.Unwrap("/nope")
	require.True(t, err != nil)
}

// --- DirReader / OpenFile ---

func TestMountFS_StatInfo_LstatInfo(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/x.txt", strings.NewReader("data")))

	fi, err := mfs.StatInfo("/x.txt")
	require.NoError(t, err)
	require.Equal(t, "x.txt", fi.Name())
	require.False(t, fi.IsDir())

	li, err := mfs.LstatInfo("/x.txt")
	require.NoError(t, err)
	require.Equal(t, "x.txt", li.Name())
}

func TestMountFS_ReadDirEntries(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/a.txt", strings.NewReader("x")))
	require.NoError(t, mfs.Mkdir("/sub"))

	entries, err := mfs.ReadDirEntries("/")
	require.NoError(t, err)
	require.True(t, len(entries) >= 2)
}

func TestMountFS_OpenFile(t *testing.T) {
	mfs, _, _ := newMount(t)
	require.NoError(t, mfs.Put("/op.txt", strings.NewReader("seed")))

	f, err := mfs.OpenFile("/op.txt", os.O_RDONLY, 0)
	require.NoError(t, err)
	defer f.Close()

	buf := make([]byte, 4)
	n, _ := f.Read(buf)
	require.Equal(t, 4, n)
	require.Equal(t, "seed", string(buf[:n]))
}

// --- AttrSetter delegation ---

func TestMountFS_Chmod_Delegates(t *testing.T) {
	mfs, _, root := newMount(t)
	require.NoError(t, mfs.Put("/m.txt", strings.NewReader("x")))

	require.NoError(t, mfs.Chmod("/m.txt", 0o600))
	info, err := os.Stat(filepath.Join(root, "m.txt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// --- No mount → error ---

func TestMountFS_NoMatchingMount(t *testing.T) {
	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/cloud", Backend: nil},
	})
	_, err := mfs.Stat("/not-in-cloud")
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "no mount"))
}

// --- Longest-prefix wins ---

func TestMountFS_LongestPrefixWins(t *testing.T) {
	beRoot, _ := fileBackendForTest(t)
	beDeep, _ := fileBackendForTest(t)

	require.NoError(t, beRoot.Put("/cloud/at-root.txt", strings.NewReader("root")))
	require.NoError(t, beDeep.Put("/inside.txt", strings.NewReader("deep")))

	mfs := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: beRoot},
		{UserPath: "/cloud/deep", Backend: beDeep},
	})

	// /cloud/at-root.txt → root backend.
	got, err := mfs.ReadFile("/cloud/at-root.txt")
	require.NoError(t, err)
	require.Equal(t, "root", string(got))

	// /cloud/deep/inside.txt → deep backend.
	got, err = mfs.ReadFile("/cloud/deep/inside.txt")
	require.NoError(t, err)
	require.Equal(t, "deep", string(got))
}
