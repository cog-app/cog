package volume

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/vfs"
)

func TestFileBackend_RenameNoReplace(t *testing.T) {
	t.Run("FreeName", func(t *testing.T) {
		b, dir := tempFileBackend(t)
		require.NoError(t, b.Put("/a.txt", strings.NewReader("a")))
		require.NoError(t, os.Chmod(filepath.Join(dir, "a.txt"), 0640))

		require.NoError(t, vfs.RenameNoReplace(b, "/a.txt", "/b.txt"))
		got, err := b.ReadFile("/b.txt")
		require.NoError(t, err)
		require.Equal(t, "a", string(got))
		_, err = os.Stat(filepath.Join(dir, "a.txt"))
		require.True(t, os.IsNotExist(err), "old name still there: %v", err)
		fi, err := os.Stat(filepath.Join(dir, "b.txt"))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0640), fi.Mode().Perm())
	})

	// A file nobody told the index about is still a file.
	t.Run("TakenName", func(t *testing.T) {
		b, dir := tempFileBackend(t)
		require.NoError(t, b.Put("/a.txt", strings.NewReader("a")))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0600))

		err := vfs.RenameNoReplace(b, "/a.txt", "/b.txt")
		require.True(t, errors.Is(err, fs.ErrExist), "got %v, want ErrExist", err)
		for name, want := range map[string]string{"/a.txt": "a", "/b.txt": "b"} {
			got, err := b.ReadFile(name)
			require.NoError(t, err)
			require.Equal(t, want, string(got))
		}
	})

	t.Run("FreeFolder", func(t *testing.T) {
		b, _ := tempFileBackend(t)
		require.NoError(t, b.Put("/d/x.txt", strings.NewReader("x")))
		require.NoError(t, vfs.RenameNoReplace(b, "/d", "/e"))
		got, err := b.ReadFile("/e/x.txt")
		require.NoError(t, err)
		require.Equal(t, "x", string(got))
	})

	// An empty folder is still something there: rename(2) would replace it.
	t.Run("TakenByEmptyFolder", func(t *testing.T) {
		b, _ := tempFileBackend(t)
		require.NoError(t, b.Put("/d/x.txt", strings.NewReader("x")))
		require.NoError(t, b.Mkdir("/e"))
		err := vfs.RenameNoReplace(b, "/d", "/e")
		require.True(t, errors.Is(err, fs.ErrExist), "got %v, want ErrExist", err)
		_, err = b.ReadFile("/d/x.txt")
		require.NoError(t, err)
	})

	t.Run("MissingSource", func(t *testing.T) {
		b, _ := tempFileBackend(t)
		err := vfs.RenameNoReplace(b, "/nope.txt", "/b.txt")
		require.True(t, errors.Is(err, fs.ErrNotExist), "got %v, want ErrNotExist", err)
		_, err = b.Stat("/b.txt")
		require.True(t, err != nil, "a missing source left something at the new name")
	})
}

// MountFS passes RenameNoReplace through to the backend.
func TestMountFS_RenameNoReplace(t *testing.T) {
	b, dir := tempFileBackend(t)
	m := NewMountFS([]MountEntry{{UserPath: "/", Backend: b}})
	require.NoError(t, b.Put("/a.txt", strings.NewReader("a")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0600))

	err := vfs.RenameNoReplace(m, "/a.txt", "/b.txt")
	require.True(t, errors.Is(err, fs.ErrExist), "got %v, want ErrExist", err)
	require.NoError(t, vfs.RenameNoReplace(m, "/a.txt", "/c.txt"))
}

// A WebDAV server refuses MOVE with Overwrite: F onto an existing resource
// with 412, which is the same as any other backend's ErrExist.
func TestWebDAVBackend_RenameNoReplace(t *testing.T) {
	var overwrite string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		overwrite = r.Header.Get("Overwrite")
		if strings.HasSuffix(r.Header.Get("Destination"), "/taken.txt") {
			w.WriteHeader(http.StatusPreconditionFailed)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	b := newWebDAVBackend(&WebDAVConfig{URL: srv.URL}, srv.Client())

	require.NoError(t, vfs.RenameNoReplace(b, "/a.txt", "/free.txt"))
	require.Equal(t, "F", overwrite)
	err := vfs.RenameNoReplace(b, "/a.txt", "/taken.txt")
	require.True(t, errors.Is(err, fs.ErrExist), "got %v, want ErrExist", err)
}
