package volume_test

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/vfs"
)

func TestCollectionEntries(t *testing.T) {
	got := volume.CollectionEntries([]string{"/b/IMG_1.jpg", "a/IMG_1.jpg", "/c/img_1.JPG", "/a/IMG_1.jpg", "/d/x"})
	require.Equal(t, []volume.CollectionEntry{
		{Name: "IMG_1.jpg", Path: "/a/IMG_1.jpg"},
		{Name: "IMG_1 (2).jpg", Path: "/b/IMG_1.jpg"},
		{Name: "img_1 (3).JPG", Path: "/c/img_1.JPG"},
		{Name: "x", Path: "/d/x"},
	}, got)
}

func TestCollectionFSInMount(t *testing.T) {
	owner, _ := fileBackendForTest(t)
	require.NoError(t, owner.Mkdir("/2019"))
	require.NoError(t, owner.Put("/2019/a.jpg", strings.NewReader("aaa")))
	require.NoError(t, owner.Put("/b.jpg", strings.NewReader("bb")))
	require.NoError(t, owner.Put("/private.jpg", strings.NewReader("no")))
	own, _ := fileBackendForTest(t)
	coll := volume.NewCollectionFS(owner, func() ([]string, error) {
		return []string{"/2019/a.jpg", "/b.jpg", "/gone.jpg"}, nil
	})
	m := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: own},
		{UserPath: "/Album", Backend: coll, ReadOnly: true, ShareOwner: "alice"},
	})

	items, err := m.List("/Album")
	require.NoError(t, err)
	var names []string
	for _, fi := range items {
		names = append(names, fi.Name+"="+fi.Path)
	}
	require.Equal(t, []string{"a.jpg=/Album/a.jpg", "b.jpg=/Album/b.jpg"}, names)

	data, err := m.ReadFile("/Album/a.jpg")
	require.NoError(t, err)
	require.Equal(t, "aaa", string(data))
	_, err = m.ReadFile("/Album/private.jpg")
	require.True(t, errors.Is(err, vfs.ErrNotFound))
	_, err = m.Stat("/Album/2019/a.jpg")
	require.True(t, errors.Is(err, vfs.ErrNotFound))

	owner2, ownerPath, ok := m.ShareTarget("/Album/a.jpg")
	require.True(t, ok)
	require.Equal(t, "alice", owner2)
	require.Equal(t, "/2019/a.jpg", ownerPath)
	_, _, ok = m.ShareTarget("/Album")
	require.False(t, ok)

	backend, sub, err := m.Unwrap("/Album/a.jpg")
	require.NoError(t, err)
	require.Equal(t, owner, backend)
	require.Equal(t, "/2019/a.jpg", sub)

	require.True(t, errors.Is(m.Put("/Album/new.jpg", strings.NewReader("x")), vfs.ErrReadOnly))
	require.True(t, errors.Is(m.Delete("/Album/a.jpg"), vfs.ErrReadOnly))
}

func newWritableCollection(t *testing.T) (owner vfs.FS, coll *volume.CollectionFS, m *volume.MountFS, paths *[]string) {
	owner, _ = fileBackendForTest(t)
	require.NoError(t, owner.Put("/2019/a.jpg", strings.NewReader("aaa")))
	require.NoError(t, owner.Put("/Up/taken.jpg", strings.NewReader("theirs")))
	list := []string{"/2019/a.jpg"}
	paths = &list
	coll = volume.NewCollectionFS(owner, func() ([]string, error) { return slices.Clone(*paths), nil })
	coll.Folder = func() (string, error) { return "/Up", nil }
	coll.Add = func(p string) error { *paths = append(*paths, p); return nil }
	coll.Remove = func(p string) error {
		*paths = slices.DeleteFunc(*paths, func(q string) bool { return q == p })
		return nil
	}
	own, _ := fileBackendForTest(t)
	m = volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: own},
		{UserPath: "/Album", Backend: coll, ShareOwner: "alice"},
	})
	return owner, coll, m, paths
}

func readString(t *testing.T, fsys vfs.FS, p string) string {
	t.Helper()
	data, err := fsys.ReadFile(p)
	require.NoError(t, err)
	return string(data)
}

func TestCollectionFSAdd(t *testing.T) {
	owner, _, m, _ := newWritableCollection(t)

	require.NoError(t, m.Mkdir("/Album"))
	require.NoError(t, m.Put("/Album/new.jpg", strings.NewReader("new")))
	require.Equal(t, "new", readString(t, owner, "/Up/new.jpg"))
	require.Equal(t, "new", readString(t, m, "/Album/new.jpg"))

	require.NoError(t, m.Put("/Album/taken.jpg", strings.NewReader("mine")))
	require.Equal(t, "mine", readString(t, owner, "/Up/taken (2).jpg"))
	require.Equal(t, "theirs", readString(t, owner, "/Up/taken.jpg"))

	f, err := m.OpenFile("/Album/sftp.jpg", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	require.NoError(t, err)
	_, err = m.Stat("/Album/sftp.jpg")
	require.NoError(t, err)
	_, err = f.Write([]byte("s"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, "s", readString(t, owner, "/Up/sftp.jpg"))

	require.True(t, errors.Is(m.Put("/Album/a.jpg", strings.NewReader("x")), vfs.ErrReadOnly))
	_, err = m.OpenFile("/Album/a.jpg", os.O_WRONLY|os.O_TRUNC, 0o644)
	require.True(t, errors.Is(err, vfs.ErrReadOnly))
	require.True(t, errors.Is(m.Mkdir("/Album/sub"), vfs.ErrReadOnly))
	require.True(t, errors.Is(m.Rename("/Album/a.jpg", "/Album/b.jpg"), vfs.ErrReadOnly))
	require.True(t, errors.Is(m.Delete("/Album/a.jpg"), vfs.ErrReadOnly))
	require.Equal(t, "aaa", readString(t, owner, "/2019/a.jpg"))
}

func TestCollectionFSModify(t *testing.T) {
	owner, coll, m, paths := newWritableCollection(t)
	coll.Modify = true

	require.NoError(t, m.Put("/Album/a.jpg", strings.NewReader("edited")))
	require.Equal(t, "edited", readString(t, owner, "/2019/a.jpg"))

	require.NoError(t, m.Rename("/Album/a.jpg", "/Album/renamed.jpg"))
	require.Equal(t, []string{"/2019/renamed.jpg"}, *paths)
	require.Equal(t, "edited", readString(t, m, "/Album/renamed.jpg"))
	_, err := owner.Stat("/2019/a.jpg")
	require.True(t, err != nil)

	require.NoError(t, m.Put("/Album/new.jpg", strings.NewReader("new")))
	require.True(t, errors.Is(m.Rename("/Album/new.jpg", "/Album/renamed.jpg"), fs.ErrExist))

	require.NoError(t, m.Delete("/Album/renamed.jpg"))
	_, err = owner.Stat("/2019/renamed.jpg")
	require.True(t, err != nil)
}
