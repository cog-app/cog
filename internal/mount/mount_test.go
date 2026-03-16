package mount_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
	"git.duckfam.us/jj-software/cog/internal/volume"
)

func defaultVolumeID(t *testing.T, env *cogtest.Env) string {
	t.Helper()
	vol, err := env.Providers.Volumes.GetByName(volume.DefaultVolumeName)
	require.NoError(t, err)
	return vol.ID
}

func TestMountCRUD(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	volID := defaultVolumeID(t, env)

	m, err := env.Providers.Mounts.Create(u.ID, "/photos", volID, "subdir", false)
	require.NoError(t, err)
	require.Equal(t, u.ID, m.UserID)
	require.Equal(t, "/photos", m.UserPath)
	require.Equal(t, volID, m.VolumeID)
	require.Equal(t, "subdir", m.VolumeSubpath)
	require.False(t, m.ReadOnly)

	got, err := env.Providers.Mounts.GetByID(m.ID)
	require.NoError(t, err)
	require.Equal(t, m.ID, got.ID)
	require.Equal(t, "/photos", got.UserPath)

	// Delete.
	require.NoError(t, env.Providers.Mounts.Delete(m.ID))
	_, err = env.Providers.Mounts.GetByID(m.ID)
	require.Error(t, err)
	require.Equal(t, mount.ErrNotFound, err)
}

func TestDeleteByUser(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	volID := defaultVolumeID(t, env)

	_, err := env.Providers.Mounts.Create(u.ID, "/a", volID, "", false)
	require.NoError(t, err)
	_, err = env.Providers.Mounts.Create(u.ID, "/b", volID, "", false)
	require.NoError(t, err)

	require.NoError(t, env.Providers.Mounts.DeleteByUser(u.ID))

	var mounts []mount.Mount
	for mt, err := range env.Providers.Mounts.ListByUser(u.ID) {
		require.NoError(t, err)
		mounts = append(mounts, mt)
	}
	require.Equal(t, 0, len(mounts))
}

func TestGetUserRoot(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")
	volID := defaultVolumeID(t, env)

	_, err := env.Providers.Mounts.Create(u.ID, "/", volID, "alice/files", false)
	require.NoError(t, err)

	root, err := env.Providers.Mounts.GetUserRoot(u.ID)
	require.NoError(t, err)
	require.Equal(t, "/", root.UserPath)
	require.Equal(t, u.ID, root.UserID)
}

func TestGetUserRootNotFound(t *testing.T) {
	env := cogtest.New(t)
	u := env.CreateUser(t, "alice", "pass")

	_, err := env.Providers.Mounts.GetUserRoot(u.ID)
	require.Error(t, err)
	require.Equal(t, mount.ErrNotFound, err)
}
