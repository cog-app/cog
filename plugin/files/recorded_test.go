package files_test

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
	"git.duckfam.us/jj-software/cog/plugin/files"
	"git.duckfam.us/jj-software/cog/vfs"
)

// withQuota gives alice, whose ID is userID, a root mount and a quota of
// limit bytes.
func withQuota(t *testing.T, env *cogtest.Env, userID string, limit int64) {
	t.Helper()
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	_, err = env.Providers.Mounts.Create(userID, "/", vol.ID, "alice/files", false)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "USER", userID, limit))
	require.NoError(t, getFilesPlugin(t, env).EnsureRootDir("alice"))
}

func openWrite(t *testing.T, fsys vfs.FS, path string, flag int) vfs.File {
	t.Helper()
	f, err := fsys.(vfs.FileOpener).OpenFile(path, flag|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	return f
}

// TestFSQuota: handles open at once share what is left of the quota, so
// two of them cannot each fill it, and an upload leaves room for what open
// handles have written.
func TestFSQuota(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	withQuota(t, env, alice.ID, 10)
	fsys, err := plug.FS(alice.Username)
	require.NoError(t, err)

	a := openWrite(t, fsys, "/a.txt", os.O_CREATE)
	b := openWrite(t, fsys, "/b.txt", os.O_CREATE)
	_, err = a.Write([]byte("123456"))
	require.NoError(t, err)
	_, err = b.WriteAt([]byte("12345"), 0)
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "second handle over the shared budget: %v", err)
	_, err = plug.Upload(alice.Username, "/c.txt", strings.NewReader("12345"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "upload beside an open handle: %v", err)
	_, err = b.Write([]byte("1234"))
	require.NoError(t, err)
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())

	used, err := plug.Usage(alice.Username)
	require.NoError(t, err)
	require.Equal(t, int64(10), used)
	meta, err := plug.GetByPath(alice.Username, "/a.txt")
	require.NoError(t, err)
	require.Equal(t, int64(6), meta.Size)

	// Truncating on open frees what the file held.
	a = openWrite(t, fsys, "/a.txt", os.O_TRUNC)
	_, err = a.Write([]byte("abcdef"))
	require.NoError(t, err)
	err = a.Truncate(7) // one byte more than there is room for
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "truncate past the quota: %v", err)
	require.NoError(t, a.Close())

	err = fsys.(vfs.AttrSetter).Truncate("/b.txt", 5)
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "truncate past the quota: %v", err)
	require.NoError(t, fsys.(vfs.AttrSetter).Truncate("/b.txt", 1))
	meta, err = plug.GetByPath(alice.Username, "/b.txt")
	require.NoError(t, err)
	require.Equal(t, int64(1), meta.Size)
}

// TestFSRecords: changes through FS keep the rows in step, including for a
// file written where the plugin did not see it.
func TestFSRecords(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	require.NoError(t, plug.EnsureRootDir(alice.Username))
	fsys, err := plug.FS(alice.Username)
	require.NoError(t, err)

	require.NoError(t, fsys.Mkdir("/x/y"))
	for _, p := range []string{"/x", "/x/y"} {
		meta, err := plug.GetByPath(alice.Username, p)
		require.NoError(t, err)
		require.True(t, meta.IsDir, p)
	}

	require.NoError(t, fsys.Put("/x/y/a.txt", strings.NewReader("hello")))
	before, err := plug.GetByPath(alice.Username, "/x/y/a.txt")
	require.NoError(t, err)
	require.NoError(t, fsys.Rename("/x/y/a.txt", "/x/b.txt"))
	after, err := plug.GetByPath(alice.Username, "/x/b.txt")
	require.NoError(t, err)
	require.Equal(t, before.ID, after.ID)

	// A rename over a file replaces its row.
	require.NoError(t, fsys.Put("/x/c.txt", strings.NewReader("old")))
	require.NoError(t, fsys.Rename("/x/b.txt", "/x/c.txt"))
	meta, err := plug.GetByPath(alice.Username, "/x/c.txt")
	require.NoError(t, err)
	require.Equal(t, before.ID, meta.ID)
	require.Equal(t, int64(5), meta.Size)

	// Written past the plugin, then renamed before anything listed it.
	raw, err := env.Providers.Plugins.UserFS(alice.Username)
	require.NoError(t, err)
	require.NoError(t, raw.Put("/unseen.txt", strings.NewReader("abc")))
	require.NoError(t, fsys.Rename("/unseen.txt", "/seen.txt"))
	meta, err = plug.GetByPath(alice.Username, "/seen.txt")
	require.NoError(t, err)
	require.Equal(t, int64(3), meta.Size)

	require.NoError(t, fsys.Delete("/x"))
	for _, p := range []string{"/x", "/x/y", "/x/c.txt"} {
		_, err := plug.GetByPath(alice.Username, p)
		require.True(t, errors.Is(err, files.ErrNotFound), "%s still has a row: %v", p, err)
	}
	items, err := plug.ListTrash(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 0, len(items))
}

// TestRenameSubtree: a directory's rename moves exactly what is beneath it.
// A LIKE prefix would also take a sibling differing in case or matching an
// underscore, and a byte length would miss beneath a non-ASCII name.
func TestRenameSubtree(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	require.NoError(t, plug.EnsureRootDir(alice.Username))

	for _, p := range []string{"/a_b/1.txt", "/A_B/2.txt", "/axb/3.txt", "/été/4.txt"} {
		_, err := plug.Upload(alice.Username, p, strings.NewReader("x"))
		require.NoError(t, err)
		// The parent directories' rows.
		_, err = plug.List(alice.Username, "/")
		require.NoError(t, err)
	}

	_, err := plug.Rename(alice.Username, "/a_b", "/moved")
	require.NoError(t, err)
	_, err = plug.Rename(alice.Username, "/été", "/summer")
	require.NoError(t, err)

	for _, p := range []string{"/moved/1.txt", "/A_B/2.txt", "/axb/3.txt", "/summer/4.txt"} {
		_, err := plug.GetByPath(alice.Username, p)
		require.NoError(t, err, p)
	}
	for _, p := range []string{"/a_b/1.txt", "/été/4.txt"} {
		_, err := plug.GetByPath(alice.Username, p)
		require.True(t, errors.Is(err, files.ErrNotFound), "%s still has a row: %v", p, err)
	}
}

// TestUploadsShareQuota: an upload in progress holds the bytes it has
// received, so another at the same time cannot be given them too.
func TestUploadsShareQuota(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	withQuota(t, env, alice.ID, 10)

	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := plug.Upload(alice.Username, "/slow.txt", r)
		done <- err
	}()
	_, err := w.Write([]byte("123456"))
	require.NoError(t, err)
	// A pipe write returns once a read has taken it, which may be before
	// the upload has charged for those bytes; the next write waits for the
	// read after, which is after the charge.
	_, err = w.Write(nil)
	require.NoError(t, err)

	_, err = plug.Upload(alice.Username, "/fast.txt", strings.NewReader("12345"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "second upload beside one in progress: %v", err)
	require.NoError(t, w.Close())
	require.NoError(t, <-done)

	_, err = plug.Upload(alice.Username, "/fast.txt", strings.NewReader("1234"))
	require.NoError(t, err)
	used, err := plug.Usage(alice.Username)
	require.NoError(t, err)
	require.Equal(t, int64(10), used)

	// A smaller replacement gives its room back.
	_, err = plug.Upload(alice.Username, "/slow.txt", strings.NewReader("1"))
	require.NoError(t, err)
	_, err = plug.Upload(alice.Username, "/more.txt", strings.NewReader("12345"))
	require.NoError(t, err)
}

// TestReconcileBothWays: listing brings rows in step with changes made
// outside the plugin (M6): a file that changed gets a new size and ETag,
// even at the same size, and one that is gone loses its row.
func TestReconcileBothWays(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	require.NoError(t, plug.EnsureRootDir(alice.Username))
	raw, err := env.Providers.Plugins.UserFS(alice.Username)
	require.NoError(t, err)

	// Folders an upload makes get rows of their own.
	_, err = plug.Upload(alice.Username, "/p/q/r.txt", strings.NewReader("r"))
	require.NoError(t, err)
	for _, dir := range []string{"/p", "/p/q"} {
		meta, err := plug.GetByPath(alice.Username, dir)
		require.NoError(t, err, dir)
		require.True(t, meta.IsDir, dir)
	}

	for _, name := range []string{"/grow.txt", "/same.txt", "/gone.txt"} {
		_, err := plug.Upload(alice.Username, name, strings.NewReader("abc"))
		require.NoError(t, err)
	}
	grow, err := plug.GetByPath(alice.Username, "/grow.txt")
	require.NoError(t, err)
	same, err := plug.GetByPath(alice.Username, "/same.txt")
	require.NoError(t, err)

	require.NoError(t, raw.Put("/grow.txt", strings.NewReader("abcdef")))
	require.NoError(t, raw.Put("/same.txt", strings.NewReader("xyz")))
	later := time.Now().Add(time.Hour)
	require.NoError(t, raw.(vfs.AttrSetter).Chtimes("/same.txt", later, later))
	require.NoError(t, raw.Delete("/gone.txt"))

	_, err = plug.List(alice.Username, "/")
	require.NoError(t, err)

	meta, err := plug.GetByPath(alice.Username, "/grow.txt")
	require.NoError(t, err)
	require.Equal(t, int64(6), meta.Size)
	require.True(t, meta.ETag != grow.ETag, "a grown file kept its ETag")
	require.Equal(t, grow.ID, meta.ID)
	meta, err = plug.GetByPath(alice.Username, "/same.txt")
	require.NoError(t, err)
	require.True(t, meta.ETag != same.ETag, "a file changed at the same size kept its ETag")
	_, err = plug.GetByPath(alice.Username, "/gone.txt")
	require.True(t, errors.Is(err, files.ErrNotFound), "a file gone from disk kept its row: %v", err)

	// Nothing changed: nothing a client sees changes either.
	_, err = plug.List(alice.Username, "/")
	require.NoError(t, err)
	again, err := plug.GetByPath(alice.Username, "/same.txt")
	require.NoError(t, err)
	require.Equal(t, meta.ETag, again.ETag)
}
