package mount_test

import (
	"testing"

	"git.duckfam.us/jj-software/cog/internal/mount"
	"git.duckfam.us/jj-software/cog/internal/testing/cogtest"
	"git.duckfam.us/jj-software/cog/internal/testing/require"
)

// A mount removed while a user's tree is being built must not stay in the
// cached tree: the build read the mounts before the removal, and caching it
// afterwards would bring the mount back until the cache expired.
func TestResolverDoesNotCacheTreeBuiltBeforeChange(t *testing.T) {
	env := cogtest.New(t)
	alice := env.CreateUser(t, "alice", "pass")
	p := env.Providers
	r := mount.NewResolver(p.Users, p.Mounts, p.Volumes, p.Access)
	m, err := p.Mounts.Create(alice.ID, "/extra", defaultVolumeID(t, env), "extra", false)
	require.NoError(t, err)

	removed := false
	mount.SetBuilt(r, func() {
		if !removed {
			removed = true
			require.NoError(t, p.Mounts.Delete(m.ID))
		}
	})
	fsys, err := r.UserFS("alice")
	require.NoError(t, err)
	_, err = fsys.Stat("/extra")
	require.NoError(t, err) // built before the removal

	fsys, err = r.UserFS("alice")
	require.NoError(t, err)
	_, err = fsys.Stat("/extra")
	require.Error(t, err)
}
