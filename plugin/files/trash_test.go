package files_test

import (
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

func readContent(t *testing.T, plug *files.Plugin, username, path string) string {
	t.Helper()
	rc, _, err := plug.Download(username, path)
	require.NoError(t, err)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	require.NoError(t, err)
	return string(b)
}

// trashOnly trashes path and returns the trash name of the one item in the
// trash.
func trashOnly(t *testing.T, plug *files.Plugin, username, path string) string {
	t.Helper()
	require.NoError(t, plug.Trash(username, path))
	items, err := plug.ListTrash(username)
	require.NoError(t, err)
	require.Equal(t, 1, len(items))
	return items[0].TrashName
}

func TestRestoreFromTrash_OriginalPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	_, err := plug.Upload(alice.Username, "/doc.txt", strings.NewReader("old"))
	require.NoError(t, err)
	name := trashOnly(t, plug, alice.Username, "/doc.txt")

	require.NoError(t, plug.RestoreFromTrash(alice.Username, name))
	require.Equal(t, "old", readContent(t, plug, alice.Username, "/doc.txt"))
}

// Restoring onto a path that has since been reused must not replace what is
// there: the trashed item comes back under a "(restored)" name instead.
func TestRestoreFromTrash_OccupiedPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	u := alice.Username

	for i, want := range []string{"/doc (restored).txt", "/doc (restored 2).txt"} {
		_, err := plug.Upload(u, "/doc.txt", strings.NewReader("trashed"))
		require.NoError(t, err)
		name := trashOnly(t, plug, u, "/doc.txt")

		current, err := plug.Upload(u, "/doc.txt", strings.NewReader("current"))
		require.NoError(t, err)
		comment, err := plug.CreateComment(u, current.ID, u, "keep me")
		require.NoError(t, err)

		require.NoError(t, plug.RestoreFromTrash(u, name), "restore %d", i)

		// The file now at the original path is untouched: same row, same
		// content, its comments intact.
		kept, err := plug.GetByPath(u, "/doc.txt")
		require.NoError(t, err)
		require.Equal(t, current.ID, kept.ID)
		require.Equal(t, "current", readContent(t, plug, u, "/doc.txt"))
		comments, err := plug.ListComments(u, current.ID)
		require.NoError(t, err)
		require.Equal(t, 1, len(comments))
		require.Equal(t, comment.ID, comments[0].ID)

		restored, err := plug.GetByPath(u, want)
		require.NoError(t, err)
		require.Equal(t, want[1:], restored.Name)
		require.Equal(t, "/", restored.ParentPath)
		require.Equal(t, "trashed", readContent(t, plug, u, want))

		items, err := plug.ListTrash(u)
		require.NoError(t, err)
		require.Equal(t, 0, len(items))

		// Clear the original path for the next round, keeping the restored copy.
		require.NoError(t, plug.HardDelete(u, "/doc.txt"))
	}
}

func TestRestoreFromTrash_OccupiedDirectory(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	u := alice.Username

	_, err := plug.CreateDir(u, "/Docs.v1")
	require.NoError(t, err)
	_, err = plug.Upload(u, "/Docs.v1/a.txt", strings.NewReader("A"))
	require.NoError(t, err)
	name := trashOnly(t, plug, u, "/Docs.v1")

	current, err := plug.CreateDir(u, "/Docs.v1")
	require.NoError(t, err)

	require.NoError(t, plug.RestoreFromTrash(u, name))

	kept, err := plug.GetByPath(u, "/Docs.v1")
	require.NoError(t, err)
	require.Equal(t, current.ID, kept.ID)

	// A folder's whole name is the stem: no extension split at the dot.
	restored, err := plug.GetByPath(u, "/Docs.v1 (restored)")
	require.NoError(t, err)
	require.True(t, restored.IsDir)
	// Its contents come back with it, picked up when the folder is listed.
	listed, err := plug.List(u, "/Docs.v1 (restored)")
	require.NoError(t, err)
	require.Equal(t, 1, len(listed))
	require.Equal(t, "A", readContent(t, plug, u, "/Docs.v1 (restored)/a.txt"))
}
