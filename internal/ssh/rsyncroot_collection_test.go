package ssh

import (
	"slices"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

// The receiver writes a temporary file and renames it into place. A shared
// album cannot take a file it did not ask for, so the temporary name must
// never reach it: the file is created whole, and the rename commits it.
func TestRsyncRootIntoCollection(t *testing.T) {
	owner, err := volume.NewBackend(&volume.Volume{URI: "file://" + t.TempDir()})
	require.NoError(t, err)
	require.NoError(t, owner.Put("/2019/a.jpg", strings.NewReader("aaa")))
	paths := []string{"/2019/a.jpg"}
	coll := volume.NewCollectionFS(owner, func() ([]string, error) { return slices.Clone(paths), nil })
	coll.Folder = func() (string, error) { return "/Up", nil }
	coll.Add = func(p string) error { paths = append(paths, p); return nil }
	home, err := volume.NewBackend(&volume.Volume{URI: "file://" + t.TempDir()})
	require.NoError(t, err)
	m := volume.NewMountFS([]volume.MountEntry{
		{UserPath: "/", Backend: home},
		{UserPath: "/files/Album", Backend: coll, ShareOwner: "alice"},
	})
	root, err := newRsyncRoot(m, "/files")
	require.NoError(t, err)

	tmp, f, err := root.CreateTemp("Album/new.jpg", 0o600)
	require.NoError(t, err)
	_, err = f.Write([]byte("new"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
	_, err = root.Lstat(tmp)
	require.True(t, err != nil, "the temporary name is not a file")
	require.NoError(t, root.Rename(tmp, "Album/new.jpg"))
	require.Equal(t, []string{"/2019/a.jpg", "/Up/new.jpg"}, paths)
	mtime := time.Date(2025, time.March, 1, 12, 0, 0, 0, time.UTC)
	require.NoError(t, root.Chtimes("Album/new.jpg", mtime, mtime))
	fi, err := root.Lstat("Album/new.jpg")
	require.NoError(t, err)
	require.Equal(t, mtime.Unix(), fi.ModTime().Unix())

	tmp, f, err = root.CreateTemp("Album/dropped.jpg", 0o600)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.NoError(t, root.Remove(tmp))
	_, err = owner.Stat("/Up/dropped.jpg")
	require.True(t, err != nil)
	items, err := owner.List("/Up")
	require.NoError(t, err)
	require.Equal(t, 1, len(items))

	_, _, err = root.CreateTemp("Album/a.jpg", 0o600)
	require.True(t, err != nil, "an Add photos share cannot replace the owner's photo")
	_, err = root.Lstat("Album/a.jpg")
	require.NoError(t, err)
}
