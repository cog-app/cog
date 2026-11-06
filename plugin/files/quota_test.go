package files_test

import (
	"errors"
	"io"
	"strings"
	"testing"

	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
	"git.duckfam.us/jj-software/cog/plugin"
)

// TestUploadQuota: an overwrite is held to the room left plus the file it
// replaces, and one that would go over the quota leaves the old file as it
// was (H3). Trashed files still count until they are deleted for good.
func TestUploadQuota(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	plug := getFilesPlugin(t, env)
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	_, err = env.Providers.Mounts.Create(alice.ID, "/", vol.ID, "alice/files", false)
	require.NoError(t, err)
	require.NoError(t, env.Providers.Quotas.Set(vol.ID, "USER", alice.ID, 10))

	_, err = plug.Upload(alice.Username, "/a.txt", strings.NewReader("12345678"))
	require.NoError(t, err)
	// 9 bytes replacing 8, with 2 left: fits, though 8+9 would not.
	_, err = plug.Upload(alice.Username, "/a.txt", strings.NewReader("123456789"))
	require.NoError(t, err)

	_, err = plug.Upload(alice.Username, "/a.txt", strings.NewReader("12345678901"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "over quota: %v", err)
	f, _, err := plug.Download(alice.Username, "/a.txt")
	require.NoError(t, err)
	got, err := io.ReadAll(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	require.Equal(t, "123456789", string(got))

	_, err = plug.Upload(alice.Username, "/b.txt", strings.NewReader("12"))
	require.True(t, errors.Is(err, plugin.ErrQuotaExceeded), "new file over quota: %v", err)
	_, err = plug.GetByPath(alice.Username, "/b.txt")
	require.True(t, err != nil, "an over-quota upload left a file")

	require.NoError(t, plug.Trash(alice.Username, "/a.txt"))
	used, err := plug.Usage(alice.Username)
	require.NoError(t, err)
	require.Equal(t, int64(9), used)
	require.NoError(t, plug.EmptyTrash(alice.Username))
	used, err = plug.Usage(alice.Username)
	require.NoError(t, err)
	require.Equal(t, int64(0), used)
}
