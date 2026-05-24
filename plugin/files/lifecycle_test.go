package files_test

import (
	"strconv"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/plugin/files"
)

// --- HardDelete ---

func TestHardDelete_File(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	uploadFile(t, env, alice, "/doomed.txt", "data")

	require.NoError(t, plug.HardDelete(alice.Username, "/doomed.txt"))

	_, err := plug.GetByPath(alice.Username, "/doomed.txt")
	require.True(t, err != nil)
}

func TestHardDelete_Directory(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	_, err := plug.CreateDir(alice.Username, "/Bin")
	require.NoError(t, err)
	uploadFile(t, env, alice, "/Bin/a.txt", "A")
	uploadFile(t, env, alice, "/Bin/b.txt", "B")

	require.NoError(t, plug.HardDelete(alice.Username, "/Bin"))

	// Parent + children all gone.
	_, err = plug.GetByPath(alice.Username, "/Bin")
	require.True(t, err != nil)
	_, err = plug.GetByPath(alice.Username, "/Bin/a.txt")
	require.True(t, err != nil)
	_, err = plug.GetByPath(alice.Username, "/Bin/b.txt")
	require.True(t, err != nil)
}

func TestHardDelete_NotFound(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	err := plug.HardDelete(alice.Username, "/never-was.txt")
	require.True(t, err != nil)
}

// --- GetByID ---

func TestGetByID_Roundtrip(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	meta := uploadFile(t, env, alice, "/x.txt", "abc")
	got, err := plug.GetByID(alice.Username, meta.ID)
	require.NoError(t, err)
	require.Equal(t, "/x.txt", got.Path)
	require.Equal(t, int64(3), got.Size)
}

func TestGetByID_Unknown(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	_, err := plug.GetByID(alice.Username, 9999999)
	require.True(t, err != nil)
}

// --- SetFavorite / SetFavoriteByID ---

func TestSetFavorite_ByPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	uploadFile(t, env, alice, "/star.txt", "x")
	require.NoError(t, plug.SetFavorite(alice.Username, "/star.txt", true))

	favs, err := plug.Favorites(alice.Username)
	require.NoError(t, err)
	require.Equal(t, 1, len(favs))

	// Unstar.
	require.NoError(t, plug.SetFavorite(alice.Username, "/star.txt", false))
	favs, _ = plug.Favorites(alice.Username)
	require.Equal(t, 0, len(favs))
}

func TestSetFavorite_MissingPath(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	err := plug.SetFavorite(alice.Username, "/never-was.txt", true)
	require.True(t, err != nil)
	require.True(t, strings.Contains(err.Error(), "not found"))
}

func TestSetFavoriteByID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	meta := uploadFile(t, env, alice, "/byid.txt", "x")
	require.NoError(t, plug.SetFavoriteByID(alice.Username, meta.ID, true))

	favs, _ := plug.Favorites(alice.Username)
	require.Equal(t, 1, len(favs))
}

func TestSetFavoriteByID_Unknown(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	err := plug.SetFavoriteByID(alice.Username, 99999999, true)
	require.True(t, err != nil)
}

// --- ParentID ---

func TestParentID_Nested(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	_, err := plug.CreateDir(alice.Username, "/Parent")
	require.NoError(t, err)
	parentMeta, _ := plug.GetByPath(alice.Username, "/Parent")
	child := uploadFile(t, env, alice, "/Parent/child.txt", "x")

	got, err := plug.ParentID(alice.Username, child.ID)
	require.NoError(t, err)
	// ParentID returns the parent's ID as a decimal string.
	require.Equal(t, strconvI(parentMeta.ID), got)
}

func TestParentID_TopLevel_ReturnsRootID(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	meta := uploadFile(t, env, alice, "/top.txt", "x")
	got, err := plug.ParentID(alice.Username, meta.ID)
	require.NoError(t, err)
	// Files directly under root have parent_path="/"; ParentID returns the
	// root directory entry's ID (a small integer, e.g. "1").
	require.True(t, got != "")
	require.True(t, got != strconvI(meta.ID))
}

func strconvI(n int64) string {
	return strconv.FormatInt(n, 10)
}

func TestParentID_Unknown(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)

	_, err := plug.ParentID(alice.Username, 99999999)
	require.True(t, err != nil)
}

// --- Start / Stop lifecycle ---

func TestPluginLifecycle_StopIsNoop(t *testing.T) {
	env := cogtest.New(t)
	plug := getFilesPlugin(t, env)

	// Stop must be safe to call (currently a no-op).
	require.NoError(t, plug.Stop())
}

// --- helpers ---

func getFilesPlugin(t testing.TB, env *cogtest.Env) *files.Plugin {
	t.Helper()
	p, ok := env.Providers.Plugins.Plugin("files")
	require.True(t, ok)
	fp, ok := p.(*files.Plugin)
	require.True(t, ok)
	return fp
}
